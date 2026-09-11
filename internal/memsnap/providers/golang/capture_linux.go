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
	"cmp"
	"container/heap"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/memsnap"
)

const (
	bucketHeaderBytes = 6 * 8
	memRecordBytes    = 4 * 4 * 8
	mbucketBatchSize  = 64
	maxVisitedBuckets = 262144
	// Bound stable stack keys retained by the global aggregate map. Map and
	// allocation metadata add overhead beyond this byte budget.
	maxAggregateKeyBytes = 32 << 20
	maxProcessReadBytes  = 1 << 20
	// Leave time for reducing aggregates, optional symbolization, and output.
	scanReserve = 20 * time.Millisecond
)

// reader reads Go runtime profiling metadata from a running process.
type reader struct {
	procRoot string
}

type processMemory struct {
	pid int
	ctx context.Context
}

type bucketRead struct {
	recordAddress uint64
	stackAddress  uint64
	stackDepth    int
	recordRaw     [memRecordBytes]byte
	objects       int64
	bytes         int64
}

type remoteRange struct {
	address uint64
	data    []byte
}

// batchWorkspace keeps all transient batch buffers bounded and reusable for
// one capture. The largest member is the 32 KiB stack slab.
type batchWorkspace struct {
	buckets      [mbucketBatchSize]bucketRead
	recordRanges [mbucketBatchSize]remoteRange
	stackRanges  [mbucketBatchSize]remoteRange
	stackBuckets [mbucketBatchSize]int
	readable     [mbucketBatchSize]bool
	local        [mbucketBatchSize]unix.Iovec
	remote       [mbucketBatchSize]unix.RemoteIovec
	stackRaw     [mbucketBatchSize * maxStackDepth * 8]byte
}

type minHeap []allocation

type allocation struct {
	key          string
	inuseBytes   int64
	inuseObjects int64
}

// allocationTotals is kept separately from the aggregate map key so each
// retained stack has only one string header.
type allocationTotals struct {
	inuseBytes   int64
	inuseObjects int64
}

func (m processMemory) readInto(address uint64, data []byte) error {
	if m.ctx != nil {
		if err := m.ctx.Err(); err != nil {
			return err
		}
	}
	if len(data) == 0 || len(data) > maxProcessReadBytes || address == 0 {
		return errors.New("process memory read range is invalid")
	}
	last := address + uint64(len(data)-1)
	if last < address || uint64(uintptr(address)) != address ||
		uint64(uintptr(last)) != last {
		return errors.New("process memory read range overflows")
	}
	local := [1]unix.Iovec{{Base: &data[0], Len: uint64(len(data))}}
	remote := [1]unix.RemoteIovec{{Base: uintptr(address), Len: len(data)}}
	read, err := unix.ProcessVMReadv(m.pid, local[:], remote[:], 0)
	if err != nil {
		return err
	}
	if m.ctx != nil {
		if err := m.ctx.Err(); err != nil {
			return err
		}
	}
	if read != len(data) {
		return fmt.Errorf("short process memory read: got %d, want %d", read,
			len(data))
	}
	return nil
}

// newReader builds a Go heap reader rooted at procRoot.
func newReader(procRoot string) *reader {
	if procRoot == "" {
		procRoot = "/proc"
	}
	return &reader{procRoot: procRoot}
}

// Capture walks the victim's mbucket chains and reduces them to a bounded
// allocation snapshot.
func (r *reader) capture(ctx context.Context,
	identity memsnap.ProcessIdentity, maxEntries int,
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
	scanDeadline, hasScanDeadline := memsnap.DeadlineWithReserve(
		ctx, scanReserve)
	var word [8]byte
	if rateAddress := target.rateAddress(); rateAddress != 0 {
		readErr := memory.readInto(rateAddress, word[:])
		if readErr == nil {
			snapshot.SampleRate = int64(byteOrder.Uint64(word[:]))
			snapshot.RateKnown = true
		}
	}
	if snapshot.RateKnown && snapshot.SampleRate <= 0 {
		if err := memsnap.ValidateIdentity(r.procRoot, identity); err != nil {
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
			if memsnap.DeadlineReached(scanDeadline, hasScanDeadline) {
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
					"mbucket header read failed after %d buckets", visitedBuckets)
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
				maxAggregateKeyBytes))
			stop = true
		}
		if stop {
			break
		}
	}
	victimExited := false
	if err := memsnap.ValidateIdentity(r.procRoot, identity); err != nil {
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
	if !victimExited && memsnap.DeadlineReached(scanDeadline, hasScanDeadline) {
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
				"symbolization unavailable: %v", ctx.Err()))
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
			clampUint64(objects), clampUint64(bytes), sampleRate)
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

// readProcessRanges combines independent victim ranges into one
// process_vm_readv call. If a range changed concurrently and causes a partial
// batch read, retry the small batch range-by-range so one bad mbucket does not
// discard its readable neighbors.
func (workspace *batchWorkspace) readProcessRanges(memory processMemory,
	ranges []remoteRange,
) []bool {
	readable := workspace.readable[:len(ranges)]
	clear(readable)
	if len(ranges) == 0 {
		return readable
	}
	local := workspace.local[:len(ranges)]
	remote := workspace.remote[:len(ranges)]
	total := 0
	valid := true
	for index := range ranges {
		rangeToRead := &ranges[index]
		if len(rangeToRead.data) == 0 || rangeToRead.address == 0 {
			valid = false
			break
		}
		local[index] = unix.Iovec{
			Base: &rangeToRead.data[0], Len: uint64(len(rangeToRead.data)),
		}
		remote[index] = unix.RemoteIovec{
			Base: uintptr(rangeToRead.address), Len: len(rangeToRead.data),
		}
		total += len(rangeToRead.data)
	}
	if valid && total <= maxProcessReadBytes {
		if memory.ctx != nil {
			if err := memory.ctx.Err(); err != nil {
				return readable
			}
		}
		read, err := unix.ProcessVMReadv(memory.pid, local, remote, 0)
		if memory.ctx != nil && memory.ctx.Err() != nil {
			return readable
		}
		if err == nil && read == total {
			for index := range readable {
				readable[index] = true
			}
			return readable
		}
	}
	for index := range ranges {
		readable[index] = memory.readInto(ranges[index].address,
			ranges[index].data) == nil
	}
	return readable
}

func decodeInUse(raw []byte, order binary.ByteOrder) (uint64, uint64) {
	var allocs, frees, allocBytes, freeBytes uint64
	for base := 0; base < memRecordBytes; base += 32 {
		allocs = memsnap.SaturatingAdd(allocs, order.Uint64(raw[base:base+8]))
		frees = memsnap.SaturatingAdd(frees, order.Uint64(raw[base+8:base+16]))
		allocBytes = memsnap.SaturatingAdd(allocBytes, order.Uint64(raw[base+16:base+24]))
		freeBytes = memsnap.SaturatingAdd(freeBytes, order.Uint64(raw[base+24:base+32]))
	}
	return posDelta(allocs, frees),
		posDelta(allocBytes, freeBytes)
}

func posDelta(left, right uint64) uint64 {
	if right > left {
		return 0
	}
	return left - right
}

func resolveStack(stack []byte, order binary.ByteOrder,
	symbolizer *symbolizer,
) []string {
	resolved := make([]string, 0, len(stack)/8)
	for offset := 0; offset < len(stack); offset += 8 {
		pc := order.Uint64(stack[offset : offset+8])
		if pc == 0 {
			break
		}
		name := symbolizer.resolve(pc)
		if name == "" {
			name = fmt.Sprintf("0x%x", pc)
		}
		resolved = append(resolved, name)
	}
	return resolved
}

func stackPCPrefix(stack []byte, order binary.ByteOrder) []byte {
	for offset := 0; offset < len(stack); offset += 8 {
		if order.Uint64(stack[offset:offset+8]) == 0 {
			return stack[:offset]
		}
	}
	return stack
}

func aggregateAllocation(aggregates map[string]int, totals *[]allocationTotals,
	stack []byte, objects, bytes int64, aggregateKeyBytes *int, maxKeyBytes int,
) bool {
	// The []byte-to-string conversion used only for map lookup does not allocate.
	// Copy the stack once only when a new aggregate needs a stable key.
	index, exists := aggregates[string(stack)]
	if !exists {
		if len(stack) > maxKeyBytes-*aggregateKeyBytes {
			return false
		}
		key := string(stack)
		index = len(*totals)
		aggregates[key] = index
		*totals = append(*totals, allocationTotals{})
		*aggregateKeyBytes += len(key)
	}
	aggregate := &(*totals)[index]
	aggregate.inuseObjects = saturatedInt64Add(aggregate.inuseObjects, objects)
	aggregate.inuseBytes = saturatedInt64Add(aggregate.inuseBytes, bytes)
	return true
}

func (h minHeap) Len() int { return len(h) }

func (h minHeap) Less(i, j int) bool {
	if h[i].inuseBytes == h[j].inuseBytes {
		return h[i].key > h[j].key
	}
	return h[i].inuseBytes < h[j].inuseBytes
}

func (h minHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *minHeap) Push(value any) { *h = append(*h, value.(allocation)) }

func (h *minHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

func keepTop(candidates *minHeap, limit int,
	candidate allocation,
) {
	if candidates.Len() < limit {
		heap.Push(candidates, candidate)
		return
	}
	worst := (*candidates)[0]
	if candidate.inuseBytes < worst.inuseBytes ||
		(candidate.inuseBytes == worst.inuseBytes && candidate.key >= worst.key) {
		return
	}
	(*candidates)[0] = candidate
	heap.Fix(candidates, 0)
}

func sortCandidates(candidates minHeap) {
	slices.SortFunc(candidates, func(left, right allocation) int {
		if byBytes := cmp.Compare(right.inuseBytes, left.inuseBytes); byBytes != 0 {
			return byBytes
		}
		return cmp.Compare(left.key, right.key)
	})
}

// scaleHeapSample follows runtime/pprof's Poisson sampling correction.
func scaleHeapSample(count, size, rate int64) (int64, int64) {
	if count == 0 || size == 0 {
		return 0, 0
	}
	if rate <= 1 {
		return count, size
	}
	averageSize := float64(size) / float64(count)
	scale := 1 / (1 - math.Exp(-averageSize/float64(rate)))
	return clampScaleToInt64(float64(count) * scale),
		clampScaleToInt64(float64(size) * scale)
}

// clampScaleToInt64 saturates the scaled sample to int64. A victim configured
// with an extreme runtime.MemProfileRate could otherwise overflow the float64
// -> int64 conversion, which is implementation-defined and yields a negative
// in-use value downstream.
func clampScaleToInt64(value float64) int64 {
	if value >= math.MaxInt64 {
		return math.MaxInt64
	}
	if value <= 0 {
		return 0
	}
	return int64(value)
}
