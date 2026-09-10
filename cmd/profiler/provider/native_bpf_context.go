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

package provider

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/profiler/bpfmap"
	"github.com/ccfos/huatuo/internal/symbol"
	"github.com/ccfos/huatuo/pkg/types"
)

// drainInterval paces ring-buffer reads. The BPF program writes events to ring A
// or B chosen by transferCnt parity; userspace flips parity each tick, then
// drains the just-frozen ring. ~100ms balances responsiveness and overhead.
const drainInterval = 100 * time.Millisecond

// validateStackID reports whether an ID returned by bpf_get_stackid can index a stack map.
// Zero is a valid key; only negative values indicate lookup errors.
func validateStackID(stackID int32) bool {
	return stackID >= 0
}

// ringBufferContext holds the shared ring buffer state for A/B buffer management.
// It encapsulates all the common infrastructure needed for dual-buffer profiling
// (readers, state map, stack maps) so profilers don't need to pass these around.
type ringBufferContext struct {
	bpf                bpf.BPF
	readerA            bpf.PerfEventReader
	readerB            bpf.PerfEventReader
	transferStateMapID uint32
	stackMapAID        uint32
	stackMapBID        uint32
	needsFallback      bool // true for memory retained mode, false for CPU/non-retained
	usym               *symbol.UsymResolver
}

// newRingBufferContext initializes the ring buffer infrastructure for dual-buffer profiling.
// It creates perf event readers for both A/B outputs and resolves map IDs for state and stack maps.
// The returned context can be used throughout the profiler's lifetime without passing individual components.
// needsFallback: true for memory retained mode (requires dual-stack-map fallback), false for others.
func newRingBufferContext(b bpf.BPF, ctx context.Context, bufferSize int, needsFallback bool) (*ringBufferContext, error) {
	readerA, err := b.EventPipeByName(ctx, "profiler_output_a", uint32(bufferSize))
	if err != nil {
		return nil, fmt.Errorf("create readerA: %w", err)
	}

	readerB, err := b.EventPipeByName(ctx, "profiler_output_b", uint32(bufferSize))
	if err != nil {
		readerA.Close()
		return nil, fmt.Errorf("create readerB: %w", err)
	}

	return &ringBufferContext{
		bpf:                b,
		readerA:            readerA,
		readerB:            readerB,
		transferStateMapID: b.MapIDByName("profiler_state_map"),
		stackMapAID:        b.MapIDByName("stack_map_a"),
		stackMapBID:        b.MapIDByName("stack_map_b"),
		needsFallback:      needsFallback,
		usym:               symbol.NewUsymResolver(),
	}, nil
}

func newSingleRingBufferContext(
	b bpf.BPF,
	ctx context.Context,
	bufferSize int,
) (*ringBufferContext, error) {
	stackMapID := b.MapIDByName("stack_map_a")
	if stackMapID == 0 {
		return nil, errors.New("stack_map_a not found")
	}

	reader, err := b.EventPipeByName(ctx, "profiler_output_a", uint32(bufferSize))
	if err != nil {
		return nil, fmt.Errorf("create readerA: %w", err)
	}

	return &ringBufferContext{
		bpf:         b,
		readerA:     reader,
		stackMapAID: stackMapID,
		usym:        symbol.NewUsymResolver(),
	}, nil
}

// Close releases the ring buffer readers. Should be called when profiling ends.
func (r *ringBufferContext) Close() {
	if r.readerA != nil {
		r.readerA.Close()
	}
	if r.readerB != nil {
		r.readerB.Close()
	}
}

// frozenRingBuffer represents a frozen ring buffer that is ready to be drained.
// It contains the reader for the ring buffer and the index to track sample counts.
// For retained mode memory profiling, fallbackStackMapID provides fallback lookup path.
type frozenRingBuffer struct {
	reader             bpf.PerfEventReader
	stackMapID         uint32
	sampleCountIdx     uint32
	fallbackStackMapID uint32 // 0 for CPU/non-retained, other stack_map for retained
}

// advanceSwapParity increments the BPF write-parity counter so the kernel
// switches to the other buffer pair, then returns the now-frozen (drainable)
// ring along with the sample-count index used to track how many events the
// BPF side wrote. The caller reads and resets that count while draining.
//
// This method uses the pre-initialized ring buffer context, eliminating the need
// to pass readerA/readerB/transferStateMapID/map names on every call.
// For retained mode (needsFallback=true), it automatically sets fallbackStackMapID.
func (r *ringBufferContext) advanceSwapParity() (frozenRingBuffer, error) {
	transferCount, err := bpfmap.ReadUint64(r.bpf, r.transferStateMapID, bpfmap.TransferCountIdx)
	if err != nil {
		return frozenRingBuffer{}, fmt.Errorf("read transferCnt: %w", err)
	}

	var ring frozenRingBuffer
	if transferCount%2 == 0 {
		ring = frozenRingBuffer{
			reader:         r.readerA,
			stackMapID:     r.stackMapAID,
			sampleCountIdx: bpfmap.SampleCountAIdx,
		}
		// Set fallback to stack_map_b for retained mode
		if r.needsFallback {
			ring.fallbackStackMapID = r.stackMapBID
		}
	} else {
		ring = frozenRingBuffer{
			reader:         r.readerB,
			stackMapID:     r.stackMapBID,
			sampleCountIdx: bpfmap.SampleCountBIdx,
		}
		// Set fallback to stack_map_a for retained mode
		if r.needsFallback {
			ring.fallbackStackMapID = r.stackMapAID
		}
	}

	if err := bpfmap.WriteUint64(r.bpf, r.transferStateMapID, bpfmap.TransferCountIdx, transferCount+1); err != nil {
		return frozenRingBuffer{}, fmt.Errorf("write transferCnt: %w", err)
	}

	return ring, nil
}

// drainFrozenRingBuffer drains events from the frozen ring buffer and aggregates raw values by stack.
// This unified method works for both CPU and Memory profilers.
func (r *ringBufferContext) drainFrozenRingBuffer(
	newEvent func() any,
) (map[processKey]map[rawStackIDs]int64, frozenRingBuffer, error) {
	ring, err := r.advanceSwapParity()
	if err != nil {
		return nil, frozenRingBuffer{}, err
	}

	// Use nested map structure for stack aggregation
	sampleCountsByProcess := make(map[processKey]map[rawStackIDs]int64)

	// Batch-read events until everything the BPF side wrote has been consumed.
	// The kernel may keep writing to the just-frozen ring briefly after the
	// parity flip, so re-check the sample count and keep draining until the
	// delivered and lost samples account for the BPF-reported count.
	var totalRead, totalAccounted uint64
	for {
		batch, err := ring.reader.ReadBatch(newEvent)
		eventCount := uint64(len(batch.Events))
		totalRead += eventCount
		// BPF increments the sample count before output, so both delivered and
		// lost samples satisfy the frozen-ring transfer protocol.
		totalAccounted += eventCount + batch.LostSamples

		for _, rec := range batch.Events {
			var base *abi.ProfilerEventBase
			switch event := rec.(type) {
			case *abi.ProfilerEventBase:
				base = event
			case *abi.ProfilerOnCPUEvent:
				base = &event.Base
			default:
				continue
			}

			// Skip events without valid stacks
			if !validateStackID(base.Kernstack) &&
				!validateStackID(base.Userstack) {
				continue
			}

			// Keep the BPF-provided unit until final aggregation (CPU: samples,
			// virtual memory: bytes, physical memory: pages).
			value := base.Value
			if value == 0 {
				continue
			}

			// Aggregate by process and stack ID
			stackIDs := rawStackIDs{KernelStackID: base.Kernstack, UserStackID: base.Userstack}
			// Extract tgid (process ID) from upper 32 bits of pid_tgid
			tgid := uint32(base.PIDTGID >> 32)
			process := processKey{PID: tgid, Comm: taskCommString(base.Comm)}

			if sampleCountsByProcess[process] == nil {
				sampleCountsByProcess[process] = make(map[rawStackIDs]int64)
			}
			sampleCountsByProcess[process][stackIDs] += value
		}

		if batch.LostSamples != 0 {
			log.Warnf("BPF perf event samples lost: %d", batch.LostSamples)
		}

		if err != nil {
			if errors.Is(err, types.ErrExitByCancelCtx) {
				return nil, frozenRingBuffer{}, err
			}
			log.WithError(err).Warn("failed to read BPF event batch")
			break
		}

		log.Debugf("drain batch: read=%d total=%d procs=%d", len(batch.Events), totalRead, len(sampleCountsByProcess))

		// An empty batch means the ring is drained for now; avoid spinning
		// even if the BPF count has not been fully matched.
		if len(batch.Events) == 0 && batch.LostSamples == 0 {
			break
		}

		bpfCount, err := bpfmap.ReadUint64(r.bpf, r.transferStateMapID, ring.sampleCountIdx)
		if err != nil {
			return nil, frozenRingBuffer{}, fmt.Errorf("read sampleCnt: %w", err)
		}

		log.Debugf(
			"drain check: totalRead=%d totalAccounted=%d bpfCount=%d",
			totalRead,
			totalAccounted,
			bpfCount,
		)

		if totalAccounted >= bpfCount {
			break
		}
	}

	log.Debugf("drain done: totalRead=%d procs=%d", totalRead, len(sampleCountsByProcess))

	if err := bpfmap.WriteUint64(r.bpf, r.transferStateMapID, ring.sampleCountIdx, 0); err != nil {
		log.Warnf("reset sample count: %v", err)
	}

	return sampleCountsByProcess, ring, nil
}

// aggregateStacksAndEnqueue resolves stack traces and emits aggregated records via enqueue callback.
// For CPU profiler, convertValue is nil (samples are already counts).
// For Memory profiler non-retained modes, convertValue converts raw value to bytes.
// For Memory profiler retained mode, fallbackStackMapID provides fallback lookup path.
//
// Stack IDs are NOT deleted from the stack map after resolution for the following reasons:
//
//  1. Caching Performance: BPF_MAP_TYPE_STACK_TRACE is a cache-like map where stack IDs
//     can be reused across multiple events. Keeping the IDs cached improves performance
//     for subsequent lookups (10-20% hit rate for repeated stacks).
//
//  2. Fallback Support: In retained mode (physical_usage), free events may reference
//     stack IDs from the previous cycle's stack_map. Deleting them would break the
//     fallback lookup path that cross-references alloc-time stacks.
//
//  3. Automatic Management: The kernel's BPF stack map implementation uses a LRU-like
//     eviction policy when the map is full, automatically managing the lifecycle of
//     stack traces without requiring explicit deletion.
//
//  4. Reduced Overhead: Deleting stack IDs requires additional BPF map operations
//     (one delete syscall per stack ID), which adds unnecessary overhead for a
//     performance-critical path. The memory overhead of keeping stale entries is
//     bounded by the map size limit (STACK_MAP_ENTRIES = 65536).
func (r *ringBufferContext) aggregateStacksAndEnqueue(
	sampleCountsByProcess map[processKey]map[rawStackIDs]int64,
	ring frozenRingBuffer,
	enqueue func(any),
	convertValue func(int64) int64,
) {
	kstackCache := make(map[int32][]string)
	ustackCache := make(map[userStackCacheKey][]string)

	var records int
	for process, stacks := range sampleCountsByProcess {
		for stackIDs, rawValue := range stacks {
			value := rawValue
			if convertValue != nil {
				value = convertValue(rawValue)
			}

			if value == 0 {
				continue
			}

			if validateStackID(stackIDs.KernelStackID) {
				if _, ok := kstackCache[stackIDs.KernelStackID]; !ok {
					kstackCache[stackIDs.KernelStackID] = r.resolveKernelStackWithFallback(
						ring,
						stackIDs.KernelStackID,
					)
				}
			}
			userCacheKey := userStackCacheKey{
				PID:     process.PID,
				StackID: stackIDs.UserStackID,
			}
			if validateStackID(stackIDs.UserStackID) {
				if _, ok := ustackCache[userCacheKey]; !ok {
					ustackCache[userCacheKey] = r.resolveUserStackWithFallback(
						ring,
						stackIDs.UserStackID,
						process.PID,
					)
				}
			}

			record := &stackSample{
				Process: process,
				StackTrace: symbolizedStackTrace{
					UserFrames:   ustackCache[userCacheKey],
					KernelFrames: kstackCache[stackIDs.KernelStackID],
				},
				Value: value,
			}

			enqueue(record)
			records++
		}
	}

	log.Debugf(
		"aggregate: procs=%d kstacks=%d ustacks=%d records=%d",
		len(sampleCountsByProcess),
		len(kstackCache),
		len(ustackCache),
		records,
	)
}

// resolveKernelStackWithFallback resolves kernel stack with fallback support.
// Fast path: lookup primary stackMapID (90-95% hit rate).
// Slow path: fallback to another stackMapID if primary lookup fails.
func (r *ringBufferContext) resolveKernelStackWithFallback(
	ring frozenRingBuffer,
	kernelStackID int32,
) []string {
	stack := r.resolveKernelStack(ring.stackMapID, kernelStackID)
	if len(stack) > 0 {
		return stack
	}

	if ring.fallbackStackMapID != 0 {
		return r.resolveKernelStack(ring.fallbackStackMapID, kernelStackID)
	}

	return nil
}

// resolveUserStackWithFallback resolves user stack with fallback support.
// Fast path: lookup primary stackMapID (90-95% hit rate).
// Slow path: fallback to another stackMapID if primary lookup fails.
func (r *ringBufferContext) resolveUserStackWithFallback(
	ring frozenRingBuffer,
	userStackID int32,
	pid uint32,
) []string {
	stack := r.resolveUserStack(ring.stackMapID, userStackID, pid)
	if len(stack) > 0 {
		return stack
	}

	if ring.fallbackStackMapID != 0 {
		return r.resolveUserStack(ring.fallbackStackMapID, userStackID, pid)
	}

	return nil
}

func (r *ringBufferContext) resolveKernelStack(stackMapID uint32, stackID int32) []string {
	if !validateStackID(stackID) {
		return nil
	}

	trace, ok := readStackTrace(r.bpf, stackMapID, stackID)
	if !ok {
		return nil
	}

	return symbol.KsymStackStrsReversed(trace[:], len(trace))
}

func (r *ringBufferContext) resolveUserStack(stackMapID uint32, stackID int32, pid uint32) []string {
	if !validateStackID(stackID) {
		return nil
	}

	trace, ok := readStackTrace(r.bpf, stackMapID, stackID)
	if !ok {
		return nil
	}

	return r.usym.UsymStackStrsReversed(pid, trace[:], len(trace))
}

func closeBPF(b bpf.BPF) error {
	if b == nil {
		return nil
	}
	return b.Close()
}
