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
	"unsafe"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler/bpfmap"
	"huatuo-bamai/internal/profiler/procutil"
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
	Pid       uint32
	Comm      [TaskCommLen]byte
	Kernstack int32
	Userstack int32
	Value     int64  // CPU: always 1 (sample count), Memory: page/byte delta
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
	needsFallback      bool  // true for memory retained mode, false for CPU/non-retained
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
	reader            bpf.PerfEventReader
	stackMapID        uint32
	sampleCountIdx    uint32
	fallbackStackMapID uint32  // 0 for CPU/non-retained, other stack_map for retained
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
	enqueue func(any),
	newEvent func() any,
	convertValue func(int64) int64,
) error {
	ring, err := r.advanceSwapParity()
	if err != nil {
		return err
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
				return err
			}
			log.Warnf("read batch: %v", err)
			break
		}

		totalRead += uint64(len(batch))

		for _, rec := range batch {
			// Directly convert to *ProfilerEventBase using pointer arithmetic.
			// This works because ProfilerEventBase is the first (embedded) field
			// in both cpuEventKey and memEvent, so they share the same memory address.
			// This is guaranteed by Go's struct layout rules for embedded fields.
			base := (*ProfilerEventBase)(unsafe.Pointer(&rec))

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
			pidName := processIDName{Pid: base.Pid, Name: procutil.CommToString(base.Comm)}

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
			return fmt.Errorf("read sampleCnt: %w", err)
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

	if len(stackCountsByProc) > 0 {
		aggregateStacksAndEnqueue(r.bpf, stackCountsByProc, ring.stackMapID, enqueue, convertValue, ring.fallbackStackMapID)
	}

	return nil
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
