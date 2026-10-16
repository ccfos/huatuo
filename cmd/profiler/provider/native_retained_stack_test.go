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
	"fmt"
	"reflect"
	"testing"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/profiler/bpfmap"
)

type retainedStackBPF struct {
	frozenRingBPFStub
	lookups []uint32
}

func (*retainedStackBPF) MapIDByName(name string) uint32 {
	return map[string]uint32{"profiler_state_map": 1, "stack_map_a": 10, "stack_map_b": 20}[name]
}

func (*retainedStackBPF) EventPipeByName(context.Context, string, uint32) (bpf.PerfEventReader, error) {
	return &frozenRingReaderStub{}, nil
}

func (b *retainedStackBPF) ReadMap(mapID uint32, key []byte) ([]byte, error) {
	if mapID == 1 {
		return b.frozenRingBPFStub.ReadMap(mapID, key)
	}
	b.lookups = append(b.lookups, mapID)
	if id := binary.LittleEndian.Uint32(key); id != 7 {
		return nil, fmt.Errorf("unexpected stack ID %d", id)
	}
	value := make([]byte, bpfmap.StackTraceLen*8)
	// Both maps contain ID 7, but only A holds the allocation's stack.
	binary.LittleEndian.PutUint64(value, uint64(mapID)*0x1000)
	if mapID == 10 {
		binary.LittleEndian.PutUint64(value[8:], 0x30000)
	}
	return value, nil
}

func TestRetainedStackIdentityAcrossOutputSwaps(t *testing.T) {
	for _, retained := range []bool{false, true} {
		t.Run(fmt.Sprintf("retained=%t", retained), func(t *testing.T) {
			stub := &retainedStackBPF{frozenRingBPFStub: frozenRingBPFStub{values: map[uint32]uint64{bpfmap.TransferCountIdx: 0}}}
			ctx, err := newRingBufferContext(stub, context.Background(), 4096, retained)
			if err != nil {
				t.Fatal(err)
			}
			defer ctx.Close()
			for parity := 0; parity < 2; parity++ {
				ring, err := ctx.advanceSwapParity()
				if err != nil {
					t.Fatal(err)
				}
				wantMap := uint32(10)
				if parity == 1 && !retained {
					wantMap = 20
				}
				if ring.stackMapID != wantMap {
					t.Fatalf("parity=%d: map=%d want=%d", parity, ring.stackMapID, wantMap)
				}
				wantReader := ctx.readerA
				wantCount := bpfmap.SampleCountAIdx
				if parity == 1 {
					wantReader = ctx.readerB
					wantCount = bpfmap.SampleCountBIdx
				}
				if ring.reader != wantReader || ring.sampleCountIdx != wantCount {
					t.Fatal("output ring did not alternate")
				}
				stub.lookups = nil
				stacks := map[processKey]map[rawStackIDs]int64{
					{PID: 0}: {{KernelStackID: 7, UserStackID: 7}: -1},
				}
				var records []*stackSample
				ctx.aggregateStacksAndEnqueue(stacks, ring, func(v any) { records = append(records, v.(*stackSample)) }, nil)
				if !reflect.DeepEqual(stub.lookups, []uint32{wantMap, wantMap}) {
					t.Fatalf("kernel/user lookups=%v, want map %d for both", stub.lookups, wantMap)
				}
				wantFrames := 1
				if wantMap == 10 {
					wantFrames = 2
				}
				if len(records) != 1 || len(records[0].StackTrace.KernelFrames) != wantFrames {
					t.Fatalf("wrong allocation stack: %+v", records)
				}
			}
		})
	}
}

func BenchmarkRetainedStackResolution(b *testing.B) {
	stub := &retainedStackBPF{lookups: make([]uint32, 0, 1)}
	ctx := &ringBufferContext{bpf: stub}
	stacks := map[processKey]map[rawStackIDs]int64{
		{PID: 1}: {{KernelStackID: 7, UserStackID: -1}: 4096},
	}
	ring := frozenRingBuffer{stackMapID: 10}
	b.ReportAllocs()
	for range b.N {
		stub.lookups = stub.lookups[:0]
		ctx.aggregateStacksAndEnqueue(stacks, ring, func(any) {}, nil)
	}
}
