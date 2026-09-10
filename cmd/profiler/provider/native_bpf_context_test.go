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
	"testing"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/profiler/bpfmap"
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
		{name: "negative ID", stackID: -1, want: false},
		{name: "zero ID", stackID: 0, want: true},
		{name: "positive ID", stackID: 1, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validateStackID(tt.stackID); got != tt.want {
				t.Fatalf("validateStackID(%d) = %t, want %t", tt.stackID, got, tt.want)
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
				return (&memNativeProfiler{bpf: obj, ringCtx: ringCtx}).Stop(nil)
			},
		},
		{
			name: "CPU",
			stop: func(ringCtx *ringBufferContext, obj *closeBPFStub) error {
				return (&cpuNativeProfiler{bpf: obj, ringCtx: ringCtx}).Stop(nil)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			readerA := &frozenRingReaderStub{}
			readerB := &frozenRingReaderStub{}
			obj := &closeBPFStub{}
			ringCtx := &ringBufferContext{readerA: readerA, readerB: readerB}

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

func (b *frozenRingBPFStub) ReadMap(_ uint32, key []byte) ([]byte, error) {
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

	got, _, err := ringCtx.drainFrozenRingBuffer(
		func() any { return &abi.ProfilerEventBase{} },
	)
	if err != nil {
		t.Fatalf("drainFrozenRingBuffer() error = %v", err)
	}
	if reader.reads != 2 {
		t.Fatalf("ReadBatch() calls = %d, want 2", reader.reads)
	}

	process := processKey{PID: 100}
	stackIDs := rawStackIDs{KernelStackID: 1, UserStackID: -1}
	if value := got[process][stackIDs]; value != 3 {
		t.Fatalf("aggregated value = %d, want 3", value)
	}
	if value := bpfStub.values[bpfmap.SampleCountAIdx]; value != 0 {
		t.Fatalf("sample count after drain = %d, want 0", value)
	}
}
