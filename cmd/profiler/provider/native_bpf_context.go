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
	"fmt"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler/bpfmap"
)

// ringBufferContext holds the shared ring buffer state for A/B buffer management.
// It encapsulates all the common infrastructure needed for dual-buffer profiling
// (readers, state map, stack maps) so profilers don't need to pass these around.
type ringBufferContext struct {
	bpf              bpf.BPF
	readerA          bpf.PerfEventReader
	readerB          bpf.PerfEventReader
	transferStateMapID uint32
	stackMapAID      uint32
	stackMapBID      uint32
}

// newRingBufferContext initializes the ring buffer infrastructure for dual-buffer profiling.
// It creates perf event readers for both A/B outputs and resolves map IDs for state and stack maps.
// The returned context can be used throughout the profiler's lifetime without passing individual components.
func newRingBufferContext(b bpf.BPF, ctx context.Context, bufferSize int) (*ringBufferContext, error) {
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
		bpf:              b,
		readerA:          readerA,
		readerB:          readerB,
		transferStateMapID: b.MapIDByName("profiler_state_map"),
		stackMapAID:      b.MapIDByName("stack_map_a"),
		stackMapBID:      b.MapIDByName("stack_map_b"),
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

// activeRing represents a frozen ring buffer that is ready to be drained.
// It contains the reader for the ring buffer and the index to track sample counts.
type activeRing struct {
	reader         bpf.PerfEventReader
	stackMapID     uint32
	sampleCountIdx uint32
}

// advanceSwapParity increments the BPF write-parity counter so the kernel
// switches to the other buffer pair, then returns the now-frozen (drainable)
// ring along with the sample-count index used to track how many events the
// BPF side wrote. The caller reads and resets that count while draining.
//
// This method uses the pre-initialized ring buffer context, eliminating the need
// to pass readerA/readerB/transferStateMapID/map names on every call.
func (r *ringBufferContext) advanceSwapParity() (activeRing, error) {
	val, err := bpfmap.ReadUint64(r.bpf, r.transferStateMapID, bpfmap.TransferCountIdx)
	if err != nil {
		return activeRing{}, fmt.Errorf("read transferCnt: %w", err)
	}

	var ring activeRing
	if val%2 == 0 {
		ring = activeRing{
			reader:         r.readerA,
			stackMapID:     r.stackMapAID,
			sampleCountIdx: bpfmap.SampleCountAIdx,
		}
	} else {
		ring = activeRing{
			reader:         r.readerB,
			stackMapID:     r.stackMapBID,
			sampleCountIdx: bpfmap.SampleCountBIdx,
		}
	}

	if err := bpfmap.WriteUint64(r.bpf, r.transferStateMapID, bpfmap.TransferCountIdx, val+1); err != nil {
		return activeRing{}, fmt.Errorf("write transferCnt: %w", err)
	}

	return ring, nil
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
