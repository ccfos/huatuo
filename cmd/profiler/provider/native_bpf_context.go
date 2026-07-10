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
	"reflect"
	"strings"
	"time"
	"unsafe"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler/bpfmap"
	"huatuo-bamai/internal/profiler/procutil"
	"huatuo-bamai/internal/symbol"
	"huatuo-bamai/pkg/types"
)

// drainTick paces ring-buffer reads. The BPF program writes events to ring A
// or B chosen by transferCnt parity; userspace flips parity each tick, then
// drains the just-frozen ring. ~100ms balances responsiveness and overhead.
const drainTick = 100 * time.Millisecond

// TaskCommLen is the length of task comm in BPF programs
const TaskCommLen = 16

// ProfilerEventBase contains the common fields shared by all profiler events.
// This matches the BPF-side struct profiler_event_base_t for binary compatibility.
type ProfilerEventBase struct {
	PidTgid   uint64 // Full pid_tgid: tgid (process) in upper 32 bits, pid (thread) in lower 32 bits
	Comm      [TaskCommLen]byte
	Kernstack int32
	Userstack int32
	Value     int64 // CPU: always 1 (sample count), Memory: page/byte delta
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

// Close releases the ring buffer readers. Should be called when profiling ends.
func (r *ringBufferContext) Close() {
	if r.readerA != nil {
		r.readerA.Close()
	}
	if r.readerB != nil {
		r.readerB.Close()
	}
}

// activeRingBuffer represents a frozen ring buffer that is ready to be drained.
// It contains the reader for the ring buffer and the index to track sample counts.
// For retained mode memory profiling, fallbackStackMapID provides fallback lookup path.
type activeRingBuffer struct {
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
func (r *ringBufferContext) advanceSwapParity() (activeRingBuffer, error) {
	val, err := bpfmap.ReadUint64(r.bpf, r.transferStateMapID, bpfmap.TransferCountIdx)
	if err != nil {
		return activeRingBuffer{}, fmt.Errorf("read transferCnt: %w", err)
	}

	var ring activeRingBuffer
	if val%2 == 0 {
		ring = activeRingBuffer{
			reader:         r.readerA,
			stackMapID:     r.stackMapAID,
			sampleCountIdx: bpfmap.SampleCountAIdx,
		}
		// Set fallback to stack_map_b for retained mode
		if r.needsFallback {
			ring.fallbackStackMapID = r.stackMapBID
		}
	} else {
		ring = activeRingBuffer{
			reader:         r.readerB,
			stackMapID:     r.stackMapBID,
			sampleCountIdx: bpfmap.SampleCountBIdx,
		}
		// Set fallback to stack_map_a for retained mode
		if r.needsFallback {
			ring.fallbackStackMapID = r.stackMapAID
		}
	}

	if err := bpfmap.WriteUint64(r.bpf, r.transferStateMapID, bpfmap.TransferCountIdx, val+1); err != nil {
		return activeRingBuffer{}, fmt.Errorf("write transferCnt: %w", err)
	}

	return ring, nil
}

// drainActiveRingBuffer drains events from the frozen ring buffer and aggregates stack traces.
// This unified method works for both CPU and Memory profilers.
//
// Parameters:
// - enqueue: callback to emit aggregated records
// - newEvent: factory function to create event struct from batch data
// - convertValue: optional function to convert raw value (nil for CPU, non-nil for Memory)
func (r *ringBufferContext) drainActiveRingBuffer(
	newEvent func() any,
	convertValue func(int64) int64,
) (map[processIDName]map[bpfmap.StackTraceID]int64, activeRingBuffer, error) {
	ring, err := r.advanceSwapParity()
	if err != nil {
		return nil, activeRingBuffer{}, err
	}

	// Use nested map structure for stack aggregation
	stackCountsByProc := make(map[processIDName]map[bpfmap.StackTraceID]int64)

	// Batch-read events until everything the BPF side wrote has been consumed.
	// The kernel may keep writing to the just-frozen ring briefly after the
	// parity flip, so re-check the sample count and keep draining until the
	// number of events read equals the BPF-reported count.
	totalRead := uint64(0)
	for {
		batch, err := ring.reader.ReadBatch(newEvent())
		if err != nil {
			if errors.Is(err, types.ErrExitByCancelCtx) {
				return nil, activeRingBuffer{}, err
			}
			log.Warnf("read batch: %v", err)
			break
		}

		totalRead += uint64(len(batch))

		for _, rec := range batch {
			// rec is a pointer to the event struct (*cpuEventKey or *ProfilerEventBase).
			// Use reflection to get the pointer value, then convert to *ProfilerEventBase.
			// For structs with embedded ProfilerEventBase, the base is at offset 0.
			ptrValue := reflect.ValueOf(rec)
			if ptrValue.Kind() != reflect.Ptr {
				continue
			}
			// Get the struct pointer and convert to *ProfilerEventBase
			base := (*ProfilerEventBase)(unsafe.Pointer(ptrValue.Pointer()))

			// Skip events without valid stacks
			if base.Kernstack <= 0 && base.Userstack <= 0 {
				continue
			}

			// Get value directly from base (CPU: 1, Memory: page/byte delta)
			value := base.Value
			if convertValue != nil {
				value = convertValue(value)
			}
			if value == 0 {
				continue
			}

			// Aggregate by process and stack ID
			pair := bpfmap.StackTraceID{KernelID: base.Kernstack, UserID: base.Userstack}
			// Extract tgid (process ID) from upper 32 bits of pid_tgid
			tgid := uint32(base.PidTgid >> 32)
			pidName := processIDName{Pid: tgid, Name: procutil.CommToString(base.Comm)}

			if stackCountsByProc[pidName] == nil {
				stackCountsByProc[pidName] = make(map[bpfmap.StackTraceID]int64)
			}
			stackCountsByProc[pidName][pair] += value
		}

		log.Debugf("drain batch: read=%d total=%d procs=%d", len(batch), totalRead, len(stackCountsByProc))

		// An empty batch means the ring is drained for now; avoid spinning
		// even if the BPF count has not been fully matched.
		if len(batch) == 0 {
			break
		}

		bpfCount, err := bpfmap.ReadUint64(r.bpf, r.transferStateMapID, ring.sampleCountIdx)
		if err != nil {
			return nil, activeRingBuffer{}, fmt.Errorf("read sampleCnt: %w", err)
		}

		log.Debugf("drain check: totalRead=%d bpfCount=%d", totalRead, bpfCount)

		if totalRead >= bpfCount {
			break
		}
	}

	log.Debugf("drain done: totalRead=%d procs=%d", totalRead, len(stackCountsByProc))

	if err := bpfmap.WriteUint64(r.bpf, r.transferStateMapID, ring.sampleCountIdx, 0); err != nil {
		log.Warnf("reset sample count: %v", err)
	}

	return stackCountsByProc, ring, nil
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
	stackCountsByProc map[processIDName]map[bpfmap.StackTraceID]int64,
	ring activeRingBuffer,
	enqueue func(any),
	convertValue func(int64) int64,
) {
	kstackCache := make(map[int32]string)
	ustackCache := make(map[int32]string)

	var records int
	for pidName, stacks := range stackCountsByProc {
		for stackID, rawValue := range stacks {
			value := rawValue
			if convertValue != nil {
				value = convertValue(rawValue)
			}

			if value == 0 {
				continue
			}

			if stackID.KernelID > 0 {
				if _, ok := kstackCache[stackID.KernelID]; !ok {
					kstackCache[stackID.KernelID] = r.resolveKstackWithFallback(ring, stackID.KernelID)
				}
			}
			if stackID.UserID > 0 {
				if _, ok := ustackCache[stackID.UserID]; !ok {
					ustackCache[stackID.UserID] = r.resolveUstackWithFallback(ring, stackID.UserID, pidName.Pid)
				}
			}

			record := &stackEntry{
				Proc:    &processIDName{Pid: pidName.Pid, Name: pidName.Name},
				User:    ustackCache[stackID.UserID],
				Kernel:  kstackCache[stackID.KernelID],
				Samples: value,
			}

			enqueue(record)
			records++
		}
	}

	log.Debugf("aggregate: procs=%d kstacks=%d ustacks=%d records=%d", len(stackCountsByProc), len(kstackCache), len(ustackCache), records)
}

// resolveKstackWithFallback resolves kernel stack with fallback support.
// Fast path: lookup primary stackMapID (90-95% hit rate).
// Slow path: fallback to another stackMapID if primary lookup fails.
func (r *ringBufferContext) resolveKstackWithFallback(ring activeRingBuffer, kernelID int32) string {
	trace, ok := readStackTrace(r.bpf, ring.stackMapID, kernelID)
	if ok {
		return strings.Join(symbol.KsymStackStrsReversed(trace[:], len(trace)), ";") + ";"
	}

	if ring.fallbackStackMapID != 0 {
		trace, ok = readStackTrace(r.bpf, ring.fallbackStackMapID, kernelID)
		if ok {
			return strings.Join(symbol.KsymStackStrsReversed(trace[:], len(trace)), ";") + ";"
		}
	}

	return ""
}

// resolveUstackWithFallback resolves user stack with fallback support.
// Fast path: lookup primary stackMapID (90-95% hit rate).
// Slow path: fallback to another stackMapID if primary lookup fails.
func (r *ringBufferContext) resolveUstackWithFallback(ring activeRingBuffer, userID int32, pid uint32) string {
	trace, ok := readStackTrace(r.bpf, ring.stackMapID, userID)
	if ok {
		return strings.Join(r.usym.UsymStackStrsReversed(pid, trace[:], len(trace)), ";") + ";"
	}

	if ring.fallbackStackMapID != 0 {
		trace, ok = readStackTrace(r.bpf, ring.fallbackStackMapID, userID)
		if ok {
			return strings.Join(r.usym.UsymStackStrsReversed(pid, trace[:], len(trace)), ";") + ";"
		}
	}

	return ""
}

// closeBpfSafe safely closes a BPF object, handling nil checks and logging errors.
func closeBpfSafe(b bpf.BPF) error {
	if b == nil {
		return nil
	}
	if err := b.Close(); err != nil {
		log.Warnf("closing eBPF: %v", err)
	}
	return nil
}
