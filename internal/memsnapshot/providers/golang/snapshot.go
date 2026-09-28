// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package golang

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

const (
	bucketHeaderBytes = 6 * 8
	maxVisitedBuckets = 262144
	// Leave time for reducing aggregates, optional symbolization, and output.
	scanReserve = 20 * time.Millisecond
)

// reader reads Go runtime profiling metadata from a running process.
type reader struct {
	procRoot string
}

// newReader builds a Go heap reader rooted at procRoot.
func newReader(procRoot string) *reader {
	if procRoot == "" {
		procRoot = "/proc"
	}
	return &reader{procRoot: procRoot}
}

// snapshot walks the victim's mbucket chains and reduces them to a bounded
// allocation snapshot.
func (r *reader) snapshot(ctx context.Context,
	identity memsnapshot.ProcessInstance, maxEntries int,
) (*snapshot, error) {
	readPID := identity.TGID
	memory := processMemory{pid: readPID, ctx: ctx}
	if maxEntries <= 0 {
		return nil, errors.New("Go mbucket limits are invalid")
	}
	target, err := discoverPID(ctx, r.procRoot, readPID)
	if err != nil {
		return nil, err
	}
	if target.startTime != identity.StartTimeTicks {
		return nil, errors.New("Go victim identity changed during address discovery")
	}
	byteOrder := target.byteOrder
	snapshot := &snapshot{RuntimeVersion: target.version}
	scanDeadline, hasScanDeadline := memsnapshot.DeadlineWithReserve(
		ctx, scanReserve,
	)
	var word [8]byte
	if rateAddress := target.rateAddress(); rateAddress != 0 {
		readErr := memory.readInto(rateAddress, word[:])
		if readErr == nil {
			snapshot.SampleRate = int64(byteOrder.Uint64(word[:]))
			snapshot.RateKnown = true
		}
	}
	if snapshot.RateKnown && snapshot.SampleRate <= 0 {
		if err := memsnapshot.ValidateProcessInstance(identity); err != nil {
			return nil, err
		}
		return snapshot, nil
	}
	err = memory.readInto(target.mbuckets(), word[:])
	if err != nil {
		return nil, fmt.Errorf("read runtime.mbuckets head: %w", err)
	}
	address := byteOrder.Uint64(word[:])
	// TopK bounds only the result. Reachable bucket stacks are read until
	// the aggregate-key budget is exhausted, so complete captures rank globally.
	aggregates := make(map[string]int)
	aggregateTotals := make([]allocationTotals, 0)
	aggregateKeyBytes := 0
	visitedBuckets := 0
	var header [bucketHeaderBytes]byte
	workspace := new(batchWorkspace)
	for address != 0 {
		batch := workspace.buckets[:0]
		stop := false
		for address != 0 && len(batch) < mbucketBatchSize {
			if err := ctx.Err(); err != nil {
				snapshot.PartialReason = "deadline reached while scanning mbucket stacks"
				stop = true
				break
			}
			if memsnapshot.DeadlineReached(scanDeadline, hasScanDeadline) {
				snapshot.PartialReason = "soft deadline reached while scanning mbucket stacks"
				stop = true
				break
			}
			if visitedBuckets >= maxVisitedBuckets {
				snapshot.PartialReason = fmt.Sprintf("mbucket safety limit %d reached",
					maxVisitedBuckets)
				stop = true
				break
			}
			current := address
			readErr := memory.readInto(current, header[:])
			if readErr != nil {
				if visitedBuckets == 0 {
					return nil, fmt.Errorf("read mbucket header %#x: %w", current, readErr)
				}
				snapshot.PartialReason = fmt.Sprintf(
					"mbucket header read failed after %d buckets", visitedBuckets,
				)
				stop = true
				break
			}
			visitedBuckets++
			next := byteOrder.Uint64(header[8:16])
			depth := byteOrder.Uint64(header[40:48])
			if depth > 0 {
				if current > math.MaxUint64-bucketHeaderBytes ||
					depth > (math.MaxUint64-current-bucketHeaderBytes)/8 {
					snapshot.PartialReason = "mbucket stack range is invalid"
					stop = true
					break
				}
				stackDepth := depth
				if stackDepth > maxStackDepth {
					stackDepth = maxStackDepth
				}
				batch = append(batch, bucketRead{
					recordAddress: current + bucketHeaderBytes + depth*8,
					stackAddress:  current + bucketHeaderBytes,
					stackDepth:    int(stackDepth),
				})
			}
			if next == current {
				snapshot.PartialReason = "mbucket chain contains a self-loop"
				stop = true
				break
			}
			address = next
		}
		if readBucketBatch(memory, batch, byteOrder, snapshot.SampleRate,
			aggregates, &aggregateTotals, &aggregateKeyBytes, snapshot, workspace) {
			appendPartialReason(snapshot, fmt.Sprintf(
				"aggregate stack-key memory limit %d bytes reached",
				maxAggregateKeyBytes,
			))
			stop = true
		}
		if stop {
			break
		}
	}
	victimExited := false
	if err := memsnapshot.ValidateProcessInstance(identity); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		victimExited = true
		appendPartialReason(snapshot,
			"Go victim exited after mbucket scan; stacks are hexadecimal")
	}
	if len(aggregates) > maxEntries {
		snapshot.OutputTruncated = true
	}
	candidates := make(minHeap, 0, min(maxEntries, len(aggregates)))
	for key, index := range aggregates {
		totals := aggregateTotals[index]
		keepTop(&candidates, maxEntries, allocation{
			key: key, inuseBytes: totals.inuseBytes,
			inuseObjects: totals.inuseObjects,
		})
	}
	sortCandidates(candidates)
	snapshot.Allocations = make([]sample, 0, len(candidates))
	if len(candidates) == 0 {
		return snapshot, nil
	}
	var symbolizer *symbolizer
	if !victimExited && memsnapshot.DeadlineReached(scanDeadline, hasScanDeadline) {
		appendPartialReason(snapshot,
			"symbolization skipped to preserve the capture deadline")
	} else if !victimExited {
		symbolizer, err = newSymbolizer(ctx, r.procPath(readPID, "exe"),
			target.loadBias, target.symbolTable)
		if err != nil {
			appendPartialReason(snapshot, fmt.Sprintf("symbolization unavailable: %v", err))
		}
	}
	for _, candidate := range candidates {
		if symbolizer != nil && ctx.Err() != nil {
			appendPartialReason(snapshot, fmt.Sprintf(
				"symbolization unavailable: %v", ctx.Err(),
			))
			symbolizer = nil
		}
		stack := resolveStack([]byte(candidate.key), byteOrder, symbolizer)
		snapshot.Allocations = append(snapshot.Allocations, sample{
			Stack: stack, Bytes: uint64(candidate.inuseBytes),
			Objects: uint64(candidate.inuseObjects),
		})
	}
	return snapshot, nil
}

func appendPartialReason(snapshot *snapshot, reason string) {
	if snapshot.PartialReason != "" {
		snapshot.PartialReason += "; "
	}
	snapshot.PartialReason += reason
}

func readBucketBatch(memory processMemory, buckets []bucketRead, order binary.ByteOrder,
	sampleRate int64, aggregates map[string]int, totals *[]allocationTotals,
	aggregateKeyBytes *int,
	snapshot *snapshot, workspace *batchWorkspace,
) bool {
	recordRanges := workspace.recordRanges[:len(buckets)]
	for index := range buckets {
		recordRanges[index] = remoteRange{
			address: buckets[index].recordAddress,
			data:    buckets[index].recordRaw[:],
		}
	}
	recordOK := workspace.readProcessRanges(memory, recordRanges)
	stackRanges := workspace.stackRanges[:0]
	stackBuckets := workspace.stackBuckets[:0]
	for index := range buckets {
		if !recordOK[index] {
			if snapshot.PartialReason == "" {
				snapshot.PartialReason = "an mbucket record became unreadable"
			}
			continue
		}
		objects, bytes := decodeInUse(buckets[index].recordRaw[:], order)
		if objects == 0 || bytes == 0 {
			continue
		}
		buckets[index].objects, buckets[index].bytes = scaleHeapSample(
			clampUint64(objects), clampUint64(bytes), sampleRate,
		)
		stackStart := index * maxStackDepth * 8
		stackRaw := workspace.stackRaw[stackStart : stackStart+buckets[index].stackDepth*8]
		stackRanges = append(stackRanges, remoteRange{
			address: buckets[index].stackAddress,
			data:    stackRaw,
		})
		stackBuckets = append(stackBuckets, index)
	}
	stackOK := workspace.readProcessRanges(memory, stackRanges)
	for rangeIndex, bucketIndex := range stackBuckets {
		if !stackOK[rangeIndex] {
			if snapshot.PartialReason == "" {
				snapshot.PartialReason = "an mbucket stack became unreadable"
			}
			continue
		}
		bucket := &buckets[bucketIndex]
		if stack := stackPCPrefix(stackRanges[rangeIndex].data, order); len(stack) != 0 {
			if !aggregateAllocation(aggregates, totals, stack, bucket.objects,
				bucket.bytes, aggregateKeyBytes, maxAggregateKeyBytes) {
				return true
			}
		}
	}
	return false
}

func (r *reader) procPath(pid int, name string) string {
	return filepath.Join(r.procRoot, strconv.Itoa(pid), name)
}
