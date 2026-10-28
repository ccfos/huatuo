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
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/cilium/ebpf"

	"github.com/ccfos/huatuo/internal/profiling/pipeline"
	"github.com/ccfos/huatuo/internal/profiling/profile"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/profiling/bpfmap"
	"github.com/ccfos/huatuo/internal/symbol"
	"github.com/ccfos/huatuo/pkg/profiling"
	"github.com/ccfos/huatuo/pkg/types"
)

type closeBPFStub struct {
	bpf.BPF
	closeErr error
	closed   bool
}

func (s *closeBPFStub) Close() error {
	s.closed = true
	return s.closeErr
}

func TestValidateStackID(t *testing.T) {
	tests := []struct {
		name    string
		stackID int32
		want    bool
	}{
		{
			name:    "negative ID",
			stackID: -1,
			want:    false,
		},
		{
			name:    "zero ID",
			stackID: 0,
			want:    true,
		},
		{
			name:    "positive ID",
			stackID: 1,
			want:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validateStackID(tt.stackID); got != tt.want {
				t.Fatalf(
					"validateStackID(%d) = %t, want %t",
					tt.stackID,
					got,
					tt.want,
				)
			}
		})
	}
}

func TestCloseBPF(t *testing.T) {
	t.Run("nil BPF", func(t *testing.T) {
		if err := closeBPF(nil); err != nil {
			t.Fatalf("closeBPF(nil) error = %v, want nil", err)
		}
	})

	t.Run("close error", func(t *testing.T) {
		wantErr := errors.New("close failed")
		stub := &closeBPFStub{closeErr: wantErr}

		if err := closeBPF(stub); !errors.Is(err, wantErr) {
			t.Fatalf("closeBPF() error = %v, want %v", err, wantErr)
		}

		if !stub.closed {
			t.Fatal("closeBPF() did not call BPF.Close")
		}
	})
}

type frozenRingReaderStub struct {
	batches  []bpf.PerfEventBatch
	reads    int
	closed   bool
	closeErr error
}

func (*frozenRingReaderStub) ReadInto(any) error {
	return nil
}

func (r *frozenRingReaderStub) ReadBatch(func() any) (bpf.PerfEventBatch, error) {
	if r.reads >= len(r.batches) {
		return bpf.PerfEventBatch{}, nil
	}

	batch := r.batches[r.reads]
	r.reads++

	return batch, nil
}

func (r *frozenRingReaderStub) Close() error {
	r.closed = true
	return r.closeErr
}

func TestNativeProfilerStopClosesReadersAndBPF(t *testing.T) {
	tests := []struct {
		name string
		stop func(*ringBufferContext, *closeBPFStub) error
	}{
		{
			name: "memory",
			stop: func(ringCtx *ringBufferContext, obj *closeBPFStub) error {
				return (&memNativeProfiler{
					bpf:     obj,
					ringCtx: ringCtx,
				}).Stop()
			},
		},
		{
			name: "CPU",
			stop: func(ringCtx *ringBufferContext, obj *closeBPFStub) error {
				return (&cpuNativeProfiler{
					bpf:     obj,
					ringCtx: ringCtx,
				}).Stop()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			readerA := &frozenRingReaderStub{}
			readerB := &frozenRingReaderStub{}
			obj := &closeBPFStub{}
			ringCtx := &ringBufferContext{
				readerA: readerA,
				readerB: readerB,
			}

			if err := tt.stop(ringCtx, obj); err != nil {
				t.Fatalf("Stop() error = %v, want nil", err)
			}

			if !readerA.closed || !readerB.closed {
				t.Fatalf("Stop() closed readers = (%t, %t), want (true, true)", readerA.closed, readerB.closed)
			}

			if !obj.closed {
				t.Fatal("Stop() did not close BPF")
			}
		})
	}
}

type frozenRingBPFStub struct {
	bpf.BPF
	values map[uint32]uint64
}

func (b *frozenRingBPFStub) ReadMap(mapID uint32, key []byte) ([]byte, error) {
	if mapID != 1 {
		return nil, ebpf.ErrKeyNotExist
	}
	value := make([]byte, 8)
	binary.LittleEndian.PutUint64(value, b.values[binary.LittleEndian.Uint32(key)])

	return value, nil
}

func (b *frozenRingBPFStub) WriteMapItems(_ uint32, items []bpf.MapItem) error {
	for _, item := range items {
		key := binary.LittleEndian.Uint32(item.Key)
		b.values[key] = binary.LittleEndian.Uint64(item.Value)
	}

	return nil
}

func TestDrainFrozenRingBufferContinuesAfterSamplesLost(t *testing.T) {
	first := &abi.ProfilerEventBase{
		PIDTGID:   uint64(100) << 32,
		Value:     1,
		Kernstack: 1,
		Userstack: -1,
	}
	second := &abi.ProfilerEventBase{
		PIDTGID:   uint64(100) << 32,
		Value:     2,
		Kernstack: 1,
		Userstack: -1,
	}
	reader := &frozenRingReaderStub{
		batches: []bpf.PerfEventBatch{
			{LostSamples: 1},
			{Events: []any{first, second}},
		},
	}
	bpfStub := &frozenRingBPFStub{
		values: map[uint32]uint64{
			bpfmap.TransferCountIdx: 0,
			bpfmap.SampleCountAIdx:  3,
		},
	}
	ringCtx := &ringBufferContext{
		bpf:                bpfStub,
		readerA:            reader,
		transferStateMapID: 1,
		stackMapAID:        2,
	}

	var total int64
	err := ringCtx.drainFrozenRingBuffer(
		func() any { return &abi.ProfilerEventBase{} },
		func(batch *pipeline.SampleBatch) error {
			for _, s := range batch.Samples {
				total += s.Value
			}

			return nil
		},
		func(value int64) (int64, error) { return value, nil },
	)
	if err != nil {
		t.Fatalf("drainFrozenRingBuffer() error = %v", err)
	}

	if reader.reads != 2 {
		t.Fatalf("ReadBatch() calls = %d, want 2", reader.reads)
	}

	if total != 3 {
		t.Fatalf("value %d want 3", total)
	}

	if value := bpfStub.values[bpfmap.SampleCountAIdx]; value != 0 {
		t.Fatalf("sample count after drain = %d, want 0", value)
	}
}

func TestUserStackCacheKeyIncludesPID(t *testing.T) {
	if (userStackCacheKey{
		PID:     100,
		StackID: 7,
	}) == (userStackCacheKey{
		PID:     200,
		StackID: 7,
	}) {
		t.Fatal("cache aliases processes")
	}
}

// Transfer-state I/O stays real to the stub while stack lookups are injected.
type stackResolutionBPF struct {
	frozenRingBPFStub
	readStack func(uint32, []byte) ([]byte, error)
}

func (s *stackResolutionBPF) ReadMap(mapID uint32, key []byte) ([]byte, error) {
	if mapID == 1 {
		return s.frozenRingBPFStub.ReadMap(mapID, key)
	}
	return s.readStack(mapID, key)
}

func TestResolveStackWithFallback(t *testing.T) {
	readErr := errors.New("stack read denied")
	for _, tt := range []struct {
		name        string
		id          int32
		primaryErr  error
		primaryRaw  []byte
		fallbackErr error
		fallback    bool
		wantErr     error
		wantMaps    []uint32
	}{
		{
			name: "negative capture ID",
			id:   -1,
		},
		{
			name:       "zero ID",
			primaryRaw: make([]byte, bpfmap.StackTraceLen*8),
			wantMaps:   []uint32{2},
		},
		{
			name:       "missing without fallback",
			primaryErr: ebpf.ErrKeyNotExist,
			wantErr:    bpfmap.ErrStackNotFound,
			wantMaps:   []uint32{2},
		},
		{
			name:     "nil lookup without fallback",
			wantErr:  bpfmap.ErrStackNotFound,
			wantMaps: []uint32{2},
		},
		{
			name:     "nil lookup uses fallback",
			fallback: true,
			wantMaps: []uint32{2, 3},
		},
		{
			name:       "wrapped missing uses fallback",
			primaryErr: fmt.Errorf("lookup: %w", ebpf.ErrKeyNotExist),
			fallback:   true,
			wantMaps:   []uint32{2, 3},
		},
		{
			name:        "both missing",
			primaryErr:  ebpf.ErrKeyNotExist,
			fallback:    true,
			fallbackErr: ebpf.ErrKeyNotExist,
			wantErr:     bpfmap.ErrStackNotFound,
			wantMaps:    []uint32{2, 3},
		},
		{
			name:        "fallback failure",
			primaryErr:  ebpf.ErrKeyNotExist,
			fallback:    true,
			fallbackErr: readErr,
			wantErr:     readErr,
			wantMaps:    []uint32{2, 3},
		},
		{
			name:       "malformed is not missing",
			primaryRaw: []byte{},
			fallback:   true,
			wantErr:    bpfmap.ErrInvalidStackData,
			wantMaps:   []uint32{2},
		},
		{
			name:       "read failure is not missing",
			primaryErr: readErr,
			fallback:   true,
			wantErr:    readErr,
			wantMaps:   []uint32{2},
		},
	} {
		for _, user := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/user=%t", tt.name, user), func(t *testing.T) {
				var maps []uint32
				obj := &stackResolutionBPF{readStack: func(mapID uint32, key []byte) ([]byte, error) {
					maps = append(maps, mapID)
					if binary.LittleEndian.Uint32(key) != uint32(tt.id) {
						t.Fatal("wrong stack ID")
					}
					if mapID == 2 {
						return tt.primaryRaw, tt.primaryErr
					}
					return make([]byte, bpfmap.StackTraceLen*8), tt.fallbackErr
				}}
				r := &ringBufferContext{
					bpf:  obj,
					usym: symbol.NewUsymResolver(),
				}
				ring := frozenRingBuffer{stackMapID: 2}
				if tt.fallback {
					ring.fallbackStackMapID = 3
				}
				var err error
				if user {
					_, err = r.resolveUserStackWithFallback(ring, tt.id, 1)
				} else {
					_, err = r.resolveKernelStackWithFallback(ring, tt.id)
				}
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
				if !slices.Equal(maps, tt.wantMaps) {
					t.Fatalf("read maps = %v, want %v", maps, tt.wantMaps)
				}
			})
		}
	}
}

func TestNativeBatchesHandleStackErrors(t *testing.T) {
	readErr := errors.New("stack read denied")
	for _, tt := range []struct {
		name    string
		raw     []byte
		readErr error
		wantErr error
	}{
		{
			name:    "missing stack",
			readErr: ebpf.ErrKeyNotExist,
		},
		{
			name: "missing stack returned as nil",
		},
		{
			name:    "malformed stack",
			raw:     []byte{},
			wantErr: bpfmap.ErrInvalidStackData,
		},
		{
			name:    "backend failure",
			readErr: readErr,
			wantErr: readErr,
		},
	} {
		for _, offCPU := range []bool{false, true} {
			for _, user := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/offcpu=%t/user=%t", tt.name, offCPU, user), func(t *testing.T) {
					obj := &stackResolutionBPF{
						frozenRingBPFStub: frozenRingBPFStub{values: map[uint32]uint64{bpfmap.SampleCountAIdx: 1}},
						readStack:         func(uint32, []byte) ([]byte, error) { return tt.raw, tt.readErr },
					}
					base := abi.ProfilerEventBase{
						Userstack: -1,
						Kernstack: 0,
						Value:     1,
					}
					if user {
						base.Userstack, base.Kernstack = 0, -1
					}
					r := &ringBufferContext{
						bpf:                obj,
						transferStateMapID: 1,
						stackMapAID:        2,
					}
					count := 0
					enqueue := func(batch *pipeline.SampleBatch) error { count += len(batch.Samples); return nil }
					var err error
					if offCPU {
						err = r.enqueueOffCPUBatch([]any{&abi.ProfilerOffCPUEvent{Base: base}}, enqueue)
					} else {
						r.readerA = &frozenRingReaderStub{batches: []bpf.PerfEventBatch{{Events: []any{&base}}}}
						err = r.drainFrozenRingBuffer(func() any { return &abi.ProfilerEventBase{} }, enqueue, func(value int64) (int64, error) { return value, nil })
					}
					if !errors.Is(err, tt.wantErr) {
						t.Fatalf("error = %v, want %v", err, tt.wantErr)
					}
					wantCount := 1
					if tt.wantErr != nil {
						wantCount = 0
					}
					if count != wantCount {
						t.Fatalf("samples delivered = %d, want %d", count, wantCount)
					}
				})
			}
		}
	}
}

type finalizationBPFStub struct {
	frozenRingBPFStub
	detached  bool
	detachErr error
	closed    bool
}

func (b *finalizationBPFStub) Detach() error { b.detached = true; return b.detachErr }
func (b *finalizationBPFStub) Close() error  { b.closed = true; return nil }

func TestNativeFinalizationDrainsBothRings(t *testing.T) {
	for _, memory := range []bool{false, true} {
		t.Run(fmt.Sprintf("memory=%t", memory), func(t *testing.T) {
			for _, parity := range []uint64{0, 1} {
				t.Run(fmt.Sprintf("parity=%d", parity), func(t *testing.T) {
					obj := &finalizationBPFStub{frozenRingBPFStub: frozenRingBPFStub{values: map[uint32]uint64{bpfmap.TransferCountIdx: parity, bpfmap.SampleCountAIdx: 1, bpfmap.SampleCountBIdx: 1}}}
					event := func(value int64) any {
						base := abi.ProfilerEventBase{
							PIDTGID:   uint64(123) << 32,
							Value:     value,
							Userstack: -1,
							Kernstack: -1,
						}
						if memory {
							return &base
						}

						return &abi.ProfilerOnCPUEvent{Base: base}
					}
					a := &frozenRingReaderStub{batches: []bpf.PerfEventBatch{{Events: []any{event(2)}}}}
					b := &frozenRingReaderStub{batches: []bpf.PerfEventBatch{{Events: []any{event(3)}}}}
					rings := &ringBufferContext{
						bpf:                obj,
						readerA:            a,
						readerB:            b,
						transferStateMapID: 1,
					}
					var p profile.Profiler
					if memory {
						p = &memNativeProfiler{
							bpf:          obj,
							ringCtx:      rings,
							internalMode: profiling.ModeVirtualAlloc,
						}
					} else {
						p = &cpuNativeProfiler{
							bpf:     obj,
							ringCtx: rings,
							config:  Config{Freq: 1000000000},
						}
					}

					var total int64
					err := p.Finalize(t.Context(), func(batch *pipeline.SampleBatch) error {
						if !obj.detached || obj.closed || a.closed || b.closed {
							t.Fatal("delivery outside detached, open resource lifetime")
						}

						for i := range batch.Samples {
							total += batch.Samples[i].Value
						}

						return nil
					})
					if err != nil || total != 5 || a.reads != 1 || b.reads != 1 {
						t.Fatalf(
							"error=%v total=%d reads=%d/%d",
							err,
							total,
							a.reads,
							b.reads,
						)
					}

					if obj.values[bpfmap.SampleCountAIdx] != 0 || obj.values[bpfmap.SampleCountBIdx] != 0 {
						t.Fatal("final samples left unaccounted")
					}

					if err := p.Stop(); err != nil {
						t.Fatal(err)
					}

					if !obj.closed || !a.closed || !b.closed {
						t.Fatal("resources not released")
					}
				})
			}
		})
	}
}

func TestNativeFinalizationFailure(t *testing.T) {
	failure := errors.New("detach failed")
	for _, tc := range []struct {
		name      string
		cancel    bool
		detachErr error
	}{
		{
			name:      "detach failure",
			detachErr: failure,
		},
		{
			name:   "cancellation",
			cancel: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := &finalizationBPFStub{detachErr: tc.detachErr}
			p := &cpuNativeProfiler{
				bpf:     obj,
				ringCtx: &ringBufferContext{},
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				cancel()
			}

			err := p.Finalize(ctx, func(*pipeline.SampleBatch) error { t.Fatal("delivered after failure"); return nil })
			want := tc.detachErr
			if tc.cancel {
				want = context.Canceled
			}

			if !errors.Is(err, want) {
				t.Fatalf("error=%v want=%v", err, want)
			}

			if err := p.Stop(); err != nil {
				t.Fatal(err)
			}

			if !obj.closed {
				t.Fatal("cleanup skipped after failure")
			}
		})
	}
}

func TestOffCPUFinalizationDrainsUntilEmpty(t *testing.T) {
	obj := &finalizationBPFStub{}
	reader := &frozenRingReaderStub{batches: []bpf.PerfEventBatch{
		{LostSamples: 2},
		{Events: []any{&abi.ProfilerOffCPUEvent{Base: abi.ProfilerEventBase{
			PIDTGID:   uint64(123) << 32,
			Value:     7,
			Userstack: -1,
			Kernstack: -1,
		}}}},
	}}
	p := &cpuNativeProfiler{
		bpf:        obj,
		ringCtx:    &ringBufferContext{readerA: reader},
		offCPUMode: true,
	}
	var total int64
	var lost uint64
	err := p.Finalize(t.Context(), func(batch *pipeline.SampleBatch) error {
		if !obj.detached || obj.closed {
			t.Fatal("invalid resource lifetime")
		}

		for i := range batch.Samples {
			total += batch.Samples[i].Value
		}

		lost += batch.DroppedSamples[types.DropReasonCollection]
		return nil
	})
	if err != nil || total != 7 || lost != 2 {
		t.Fatalf(
			"error=%v total=%d lost=%d",
			err,
			total,
			lost,
		)
	}
}

func TestNativeFinalizationAccountsForPendingLoss(t *testing.T) {
	for _, tc := range []struct {
		name     string
		expected uint64
		events   int
		lost     uint64
		wantLost uint64
	}{
		{
			name:     "missing loss record",
			expected: 3,
			wantLost: 3,
		},
		{
			name:     "partial loss record",
			expected: 5,
			events:   1,
			lost:     2,
			wantLost: 4,
		},
		{
			name:     "complete loss record",
			expected: 3,
			events:   1,
			lost:     2,
			wantLost: 2,
		},
		{
			name:     "late loss record",
			lost:     2,
			wantLost: 2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := &finalizationBPFStub{frozenRingBPFStub: frozenRingBPFStub{values: map[uint32]uint64{bpfmap.SampleCountAIdx: tc.expected}}}
			batch := bpf.PerfEventBatch{LostSamples: tc.lost}
			for range tc.events {
				batch.Events = append(batch.Events, &abi.ProfilerOnCPUEvent{Base: abi.ProfilerEventBase{
					Value:     1,
					Userstack: -1,
					Kernstack: -1,
				}})
			}

			p := &cpuNativeProfiler{
				bpf:    obj,
				config: Config{Freq: 99},
				ringCtx: &ringBufferContext{
					bpf:                obj,
					readerA:            &frozenRingReaderStub{batches: []bpf.PerfEventBatch{batch}},
					readerB:            &frozenRingReaderStub{},
					transferStateMapID: 1,
				},
			}
			var dropped uint64
			err := p.Finalize(t.Context(), func(batch *pipeline.SampleBatch) error {
				dropped += batch.DroppedSamples[types.DropReasonCollection]
				return nil
			})
			if err != nil || dropped != tc.wantLost || obj.values[bpfmap.SampleCountAIdx] != 0 {
				t.Fatalf(
					"error=%v lost=%d want=%d remaining=%d",
					err,
					dropped,
					tc.wantLost,
					obj.values[bpfmap.SampleCountAIdx],
				)
			}
		})
	}
}

func TestNativeFinalizationPreservesCountOnDeliveryFailure(t *testing.T) {
	failure := errors.New("loss delivery failed")
	obj := &finalizationBPFStub{frozenRingBPFStub: frozenRingBPFStub{values: map[uint32]uint64{bpfmap.SampleCountAIdx: 3}}}
	p := &cpuNativeProfiler{
		bpf:    obj,
		config: Config{Freq: 99},
		ringCtx: &ringBufferContext{
			bpf:                obj,
			readerA:            &frozenRingReaderStub{},
			readerB:            &frozenRingReaderStub{},
			transferStateMapID: 1,
		},
	}
	err := p.Finalize(t.Context(), func(*pipeline.SampleBatch) error { return failure })
	if !errors.Is(err, failure) || obj.values[bpfmap.SampleCountAIdx] != 3 {
		t.Fatalf("error=%v remaining=%d", err, obj.values[bpfmap.SampleCountAIdx])
	}
}

type taskReaderBPFStub struct {
	bpf.BPF
	readers []*taskReaderStub
}

func (*taskReaderBPFStub) MapIDByName(string) uint32 { return 1 }
func (b *taskReaderBPFStub) EventPipeByName(ctx context.Context, _ string, _ uint32) (bpf.PerfEventReader, error) {
	r := &taskReaderStub{
		ctx:     ctx,
		reading: make(chan struct{}),
	}
	b.readers = append(b.readers, r)
	return r, nil
}

type taskReaderStub struct {
	ctx     context.Context
	reading chan struct{}
}

func (*taskReaderStub) ReadInto(any) error { return nil }
func (*taskReaderStub) Close() error       { return nil }
func (r *taskReaderStub) ReadBatch(func() any) (bpf.PerfEventBatch, error) {
	close(r.reading)
	<-r.ctx.Done()
	return bpf.PerfEventBatch{}, types.ErrExitByCancelCtx
}

func TestNativeReadersFollowTaskLifetime(t *testing.T) {
	for _, single := range []bool{false, true} {
		t.Run(fmt.Sprintf("single=%t", single), func(t *testing.T) {
			taskCtx, abort := context.WithCancel(t.Context())
			defer abort()
			collectionCtx, stop := context.WithCancelCause(taskCtx)
			defer stop(nil)
			obj := &taskReaderBPFStub{}
			var err error
			if single {
				_, err = newSingleRingBufferContext(obj, taskCtx, 4096)
			} else {
				_, err = newRingBufferContext(
					obj,
					taskCtx,
					4096,
					false,
				)
			}

			if err != nil {
				t.Fatal(err)
			}

			stop(profile.ErrStopRequested)
			if !profile.IsNormalStop(collectionCtx) {
				t.Fatal("collection did not stop normally")
			}

			for _, r := range obj.readers {
				if r.ctx.Err() != nil {
					t.Fatal("normal stop canceled reader")
				}
			}

			done := make(chan error, 1)
			go func() {
				_, err := obj.readers[0].ReadBatch(func() any { return nil })
				done <- err
			}()
			<-obj.readers[0].reading
			abort()
			select {
			case err := <-done:
				if !errors.Is(err, types.ErrExitByCancelCtx) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("task cancellation did not interrupt active reading")
			}

			for _, r := range obj.readers {
				if !errors.Is(r.ctx.Err(), context.Canceled) {
					t.Fatal("task cancellation did not reach reader")
				}
			}
		})
	}
}

func BenchmarkDrainFrozenRingBuffer(b *testing.B) {
	obj := &frozenRingBPFStub{values: map[uint32]uint64{}}
	event := &abi.ProfilerEventBase{
		Value:     1,
		Userstack: -1,
		Kernstack: -1,
	}
	a := &frozenRingReaderStub{batches: []bpf.PerfEventBatch{{Events: []any{event}}}}
	c := &frozenRingReaderStub{batches: []bpf.PerfEventBatch{{Events: []any{event}}}}
	r := &ringBufferContext{
		bpf:                obj,
		readerA:            a,
		readerB:            c,
		transferStateMapID: 1,
	}
	newEvent := func() any { return &abi.ProfilerEventBase{} }
	enqueue := func(*pipeline.SampleBatch) error { return nil }
	convert := func(value int64) (int64, error) { return value, nil }
	b.ReportAllocs()
	for b.Loop() {
		a.reads, c.reads = 0, 0
		obj.values[bpfmap.SampleCountAIdx], obj.values[bpfmap.SampleCountBIdx] = 1, 1
		if err := r.drainFrozenRingBuffer(newEvent, enqueue, convert); err != nil {
			b.Fatal(err)
		}
	}
}

func TestNativeFinalizationAccountsAcrossWindows(t *testing.T) {
	obj := &finalizationBPFStub{frozenRingBPFStub: frozenRingBPFStub{values: map[uint32]uint64{bpfmap.SampleCountAIdx: 3}}}
	a := &frozenRingReaderStub{batches: []bpf.PerfEventBatch{{}}}
	r := &ringBufferContext{
		bpf:                obj,
		readerA:            a,
		readerB:            &frozenRingReaderStub{},
		transferStateMapID: 1,
	}
	p := &cpuNativeProfiler{
		bpf:     obj,
		config:  Config{Freq: 99},
		ringCtx: r,
	}
	var dropped uint64
	enqueue := func(batch *pipeline.SampleBatch) error {
		dropped += batch.DroppedSamples[types.DropReasonCollection]
		return nil
	}
	if err := r.drainFrozenRingBuffer(func() any { return &abi.ProfilerOnCPUEvent{} }, enqueue, p.convertCPUValue); err != nil {
		t.Fatal(err)
	}
	// Three failures from the previous window are reported only when a later
	// success emits their perf loss record; two new failures remain unreported.
	obj.values[bpfmap.SampleCountAIdx] = 3
	a.reads = 0
	a.batches = []bpf.PerfEventBatch{{
		LostSamples: 3,
		Events: []any{&abi.ProfilerOnCPUEvent{Base: abi.ProfilerEventBase{
			Value:     1,
			Userstack: -1,
			Kernstack: -1,
		}}},
	}}
	if err := p.Finalize(t.Context(), enqueue); err != nil {
		t.Fatal(err)
	}

	if dropped != 5 {
		t.Fatalf("lost=%d want 5", dropped)
	}
}

func TestNativeStopPreservesAllCloseFailures(t *testing.T) {
	for _, memory := range []bool{false, true} {
		t.Run(fmt.Sprintf("memory=%t", memory), func(t *testing.T) {
			firstErr := errors.New("reader A close failed")
			secondErr := errors.New("reader B close failed")
			bpfErr := errors.New("BPF close failed")
			first := &frozenRingReaderStub{closeErr: firstErr}
			second := &frozenRingReaderStub{closeErr: secondErr}
			obj := &closeBPFStub{closeErr: bpfErr}
			rings := &ringBufferContext{
				readerA: first,
				readerB: second,
			}
			var collector profile.Profiler
			if memory {
				collector = &memNativeProfiler{
					bpf:     obj,
					ringCtx: rings,
				}
			} else {
				collector = &cpuNativeProfiler{
					bpf:     obj,
					ringCtx: rings,
				}
			}

			err := collector.Stop()
			for _, want := range []error{firstErr, secondErr, bpfErr} {
				if !errors.Is(err, want) {
					t.Fatalf("Stop() error=%v, missing %v", err, want)
				}
			}

			if !first.closed || !second.closed || !obj.closed {
				t.Fatal("Close was not attempted for every resource")
			}

			if err := collector.Stop(); err != nil {
				t.Fatalf("repeated Stop() error=%v", err)
			}
		})
	}
}

type readerCreationFailureBPF struct {
	bpf.BPF
	reader *frozenRingReaderStub
	err    error
}

func (b *readerCreationFailureBPF) EventPipeByName(_ context.Context, name string, _ uint32) (bpf.PerfEventReader, error) {
	if name == "profiler_output_a" {
		return b.reader, nil
	}

	return nil, b.err
}

func TestNativeReaderCreationPreservesCleanupFailure(t *testing.T) {
	createErr := errors.New("reader B creation failed")
	closeErr := errors.New("reader A close failed")
	first := &frozenRingReaderStub{closeErr: closeErr}
	obj := &readerCreationFailureBPF{
		reader: first,
		err:    createErr,
	}
	_, err := newRingBufferContext(obj, t.Context(), 4096, false)
	if !errors.Is(err, createErr) || !errors.Is(err, closeErr) || !first.closed {
		t.Fatalf("creation error=%v cleanup attempted=%t", err, first.closed)
	}
}
