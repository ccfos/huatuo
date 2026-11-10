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
	"math"
	"time"

	"github.com/ccfos/huatuo/internal/profiling/pipeline"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/profiling/bpfmap"
	"github.com/ccfos/huatuo/internal/symbol"
	"github.com/ccfos/huatuo/pkg/profiling"
	"github.com/ccfos/huatuo/pkg/types"
)

// drainInterval paces ring-buffer reads. The BPF program writes events to ring A
// or B chosen by transferCnt parity; userspace flips parity each tick, then
// drains the just-frozen ring. ~100ms balances responsiveness and overhead.
const drainInterval = 100 * time.Millisecond

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
	mode               profiling.Mode
	usym               *symbol.UsymResolver
	synchronizeWriters func() error

	// Perf loss records may arrive in a later window than their failed outputs.
	sampleAttempts   uint64
	accountedSamples uint64
}

// newRingBufferContext initializes the ring buffer infrastructure for dual-buffer profiling.
// It creates perf event readers for both A/B outputs and resolves map IDs for state and stack maps.
// The returned context can be used throughout the profiler's lifetime without passing individual components.
// The mode determines whether retained allocation stacks need fallback lookup.
func newRingBufferContext(
	b bpf.BPF,
	ctx context.Context,
	bufferSize int,
	mode profiling.Mode,
) (*ringBufferContext, error) {
	return newRingBufferContextWithSynchronization(b, ctx, bufferSize, mode, detectNativeWriterSynchronization())
}

func newRingBufferContextWithSynchronization(
	b bpf.BPF, ctx context.Context, bufferSize int, mode profiling.Mode,
	synchronize func() error,
) (*ringBufferContext, error) {
	// Start supplies the task context, so failure and timeout interrupt reads.
	readerA, err := b.EventPipeByName(ctx, "profiler_output_a", uint32(bufferSize))
	if err != nil {
		return nil, fmt.Errorf("create readerA: %w", err)
	}

	readerB, err := b.EventPipeByName(ctx, "profiler_output_b", uint32(bufferSize))
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("create readerB: %w", err),
			(&ringBufferContext{readerA: readerA}).Close(),
		)
	}

	return &ringBufferContext{
		bpf:                b,
		readerA:            readerA,
		readerB:            readerB,
		transferStateMapID: b.MapIDByName("profiler_state_map"),
		stackMapAID:        b.MapIDByName("stack_map_a"),
		stackMapBID:        b.MapIDByName("stack_map_b"),
		mode:               mode,
		synchronizeWriters: synchronize,
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

	// Start supplies the task context, so failure and timeout interrupt reads.
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
func (r *ringBufferContext) Close() error {
	var readerAErr, readerBErr error
	if r.readerA != nil {
		if err := r.readerA.Close(); err != nil {
			readerAErr = fmt.Errorf("close native reader A: %w", err)
		}

		r.readerA = nil
	}

	if r.readerB != nil {
		if err := r.readerB.Close(); err != nil {
			readerBErr = fmt.Errorf("close native reader B: %w", err)
		}

		r.readerB = nil
	}

	return errors.Join(readerAErr, readerBErr)
}

// nativeRingBuffer identifies one output and its associated maps.
// It contains the reader for the ring buffer and the index to track sample counts.
// For retained mode memory profiling, fallbackStackMapID provides fallback lookup path.
type nativeRingBuffer struct {
	readOptions        []bpf.ReadBatchOption // Set only after writer synchronization or detach.
	reader             bpf.PerfEventReader
	stackMapID         uint32
	sampleCountIdx     uint32
	fallbackStackMapID uint32 // 0 for CPU/non-retained, other stack_map for retained
}

// advanceSwapParity increments the BPF write-parity counter so the kernel
// switches new writers to the other buffer pair, then returns the previous
// ring. Existing writers may still use it until synchronized. The index tracks events the
// BPF side wrote. The caller reads and resets that count while draining.
//
// This method uses the pre-initialized ring buffer context, eliminating the need
// to pass readerA/readerB/transferStateMapID/map names on every call.
// Physical usage retains allocation stacks across both maps.
func (r *ringBufferContext) advanceSwapParity() (nativeRingBuffer, error) {
	transferCount, err := bpfmap.ReadUint64(r.bpf, r.transferStateMapID, bpfmap.TransferCountIdx)
	if err != nil {
		return nativeRingBuffer{}, fmt.Errorf("read transferCnt: %w", err)
	}

	var ring nativeRingBuffer
	if transferCount%2 == 0 {
		ring = nativeRingBuffer{
			reader:         r.readerA,
			stackMapID:     r.stackMapAID,
			sampleCountIdx: bpfmap.SampleCountAIdx,
		}
		// Set fallback to stack_map_b for retained mode
		if r.mode == profiling.ModePhysicalUsage {
			ring.fallbackStackMapID = r.stackMapBID
		}
	} else {
		ring = nativeRingBuffer{
			reader:         r.readerB,
			stackMapID:     r.stackMapBID,
			sampleCountIdx: bpfmap.SampleCountBIdx,
		}
		// Set fallback to stack_map_a for retained mode
		if r.mode == profiling.ModePhysicalUsage {
			ring.fallbackStackMapID = r.stackMapAID
		}
	}

	if err := bpfmap.WriteUint64(
		r.bpf,
		r.transferStateMapID,
		bpfmap.TransferCountIdx,
		transferCount+1,
	); err != nil {
		return nativeRingBuffer{}, fmt.Errorf("write transferCnt: %w", err)
	}

	return ring, nil
}

// swapAndDrainRingBuffer preserves the A/B transfer boundary while delivering
// bounded symbolized batches. Unsupported synchronization uses legacy blocking reads.
func (r *ringBufferContext) swapAndDrainRingBuffer(
	newEvent func() any,
	enqueue func(*pipeline.SampleBatch) error,
	convert func(int64) (int64, error),
) error {
	ring, err := r.advanceSwapParity()
	if err != nil {
		return err
	}

	if r.synchronizeWriters != nil {
		if err := r.synchronizeWriters(); err != nil {
			return fmt.Errorf("synchronize frozen native ring: %w", err)
		}
		ring.readOptions = []bpf.ReadBatchOption{bpf.WithReadBatchDeadline(time.Now())}
	}

	accounted, expected, err := r.drainRingBuffer(
		ring,
		newEvent,
		enqueue,
		convert,
	)
	if err != nil {
		return err
	}

	if err := r.accountSamples(expected, accounted); err != nil {
		return err
	}

	return bpfmap.WriteUint64(
		r.bpf,
		r.transferStateMapID,
		ring.sampleCountIdx,
		0,
	)
}

func (r *ringBufferContext) drainRingBuffer(
	ring nativeRingBuffer,
	newEvent func() any,
	enqueue func(*pipeline.SampleBatch) error,
	convert func(int64) (int64, error),
) (accounted, expected uint64, returnErr error) {
	processor := nativeSampleProcessor{
		mode: r.mode,
		stacks: nativeStackResolver{
			bpf:                r.bpf,
			usym:               r.usym,
			stackMapID:         ring.stackMapID,
			fallbackStackMapID: ring.fallbackStackMapID,
		},
		kernelCache: make(map[int32][]types.ProfileFrame),
		userCache:   make(map[userStackCacheKey][]types.ProfileFrame),
	}
	for {
		batch, readErr := ring.reader.ReadBatch(newEvent, ring.readOptions...)
		if batch.LostSamples > math.MaxUint64-uint64(len(batch.Events)) ||
			uint64(len(batch.Events))+batch.LostSamples > math.MaxUint64-accounted {
			return accounted, expected, fmt.Errorf("BPF transfer event count overflows uint64")
		}

		accounted += uint64(len(batch.Events)) + batch.LostSamples
		if err := processor.enqueueSamples(batch.Events, convert, enqueue); err != nil {
			return accounted, expected, err
		}

		if batch.LostSamples != 0 {
			if err := enqueue(&pipeline.SampleBatch{
				DroppedSamples: map[string]uint64{types.DropReasonCollection: batch.LostSamples},
			}); err != nil {
				return accounted, expected, err
			}
		}

		if readErr != nil {
			return accounted, expected, readErr
		}

		count, err := bpfmap.ReadUint64(r.bpf, r.transferStateMapID, ring.sampleCountIdx)
		if err != nil {
			return accounted, expected, fmt.Errorf("read frozen sample count: %w", err)
		}

		expected = count
		empty := len(batch.Events) == 0 && batch.LostSamples == 0
		if empty || accounted >= expected {
			break
		}
	}

	return accounted, expected, nil
}

// drainFinalRingBuffers drains both rings after producers detach and accounts
// for losses not reported by perf, keeping stack maps live during delivery.
func (r *ringBufferContext) drainFinalRingBuffers(
	ctx context.Context,
	newEvent func() any,
	enqueue func(*pipeline.SampleBatch) error,
	convert func(int64) (int64, error),
) error {
	deliver := func(batch *pipeline.SampleBatch) error {
		if err := ctx.Err(); err != nil {
			return err
		}

		return enqueue(batch)
	}
	var countIndices [2]uint32
	for i := range countIndices {
		if err := ctx.Err(); err != nil {
			return err
		}

		ring, err := r.advanceSwapParity()
		if err != nil {
			return err
		}

		ring.readOptions = []bpf.ReadBatchOption{bpf.WithReadBatchDeadline(time.Now())}

		// Production has stopped: counts include failed perf output attempts
		// whose loss record may never arrive without another kernel event.
		accounted, expected, err := r.drainRingBuffer(
			ring,
			newEvent,
			deliver,
			convert,
		)
		if err != nil {
			return err
		}

		if err := r.accountSamples(expected, accounted); err != nil {
			return err
		}

		countIndices[i] = ring.sampleCountIdx
	}

	if r.sampleAttempts > r.accountedSamples {
		if err := deliver(&pipeline.SampleBatch{
			DroppedSamples: map[string]uint64{types.DropReasonCollection: r.sampleAttempts - r.accountedSamples},
		}); err != nil {
			return err
		}
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	for _, index := range countIndices {
		if err := bpfmap.WriteUint64(
			r.bpf,
			r.transferStateMapID,
			index,
			0,
		); err != nil {
			return err
		}
	}

	r.sampleAttempts, r.accountedSamples = 0, 0
	return nil
}

func (r *ringBufferContext) accountSamples(expected, accounted uint64) error {
	if expected > math.MaxUint64-r.sampleAttempts || accounted > math.MaxUint64-r.accountedSamples {
		return errors.New("native sample accounting overflows uint64")
	}

	r.sampleAttempts += expected
	r.accountedSamples += accounted
	return nil
}

func closeBPF(b bpf.BPF) error {
	if b == nil {
		return nil
	}

	return b.Close()
}
