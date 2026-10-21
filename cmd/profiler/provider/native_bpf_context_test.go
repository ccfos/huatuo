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
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/cilium/ebpf"

	"github.com/ccfos/huatuo/internal/profiling/pipeline"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/profiling/bpfmap"
	"github.com/ccfos/huatuo/internal/symbol"
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
	batches []bpf.PerfEventBatch
	reads   int
	closed  bool
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
	return nil
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
			wantErr:    ebpf.ErrKeyNotExist,
			wantMaps:   []uint32{2},
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
			wantErr:     ebpf.ErrKeyNotExist,
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
			name:     "malformed is not missing",
			fallback: true,
			wantErr:  bpfmap.ErrInvalidStackData,
			wantMaps: []uint32{2},
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
		readErr error
		wantErr error
	}{
		{
			name:    "missing stack",
			readErr: ebpf.ErrKeyNotExist,
		},
		{
			name:    "malformed stack",
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
						readStack:         func(uint32, []byte) ([]byte, error) { return nil, tt.readErr },
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
