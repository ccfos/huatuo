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
	"reflect"
	"testing"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/bpf/abi"
	"huatuo-bamai/pkg/profiling"
)

type memoryPipelineBPFStub struct {
	bpf.BPF
	state [3]uint64
}

func (s *memoryPipelineBPFStub) ReadMap(mapID uint32, key []byte) ([]byte, error) {
	if mapID != 1 {
		return nil, nil
	}

	idx := binary.LittleEndian.Uint32(key)
	value := make([]byte, 8)
	binary.LittleEndian.PutUint64(value, s.state[idx])
	return value, nil
}

func (s *memoryPipelineBPFStub) WriteMapItems(mapID uint32, items []bpf.MapItem) error {
	if mapID != 1 {
		return nil
	}

	for _, item := range items {
		idx := binary.LittleEndian.Uint32(item.Key)
		s.state[idx] = binary.LittleEndian.Uint64(item.Value)
	}
	return nil
}

type memoryPipelineReaderStub struct {
	batch []any
}

func (s *memoryPipelineReaderStub) ReadInto(any) error {
	return nil
}

func (s *memoryPipelineReaderStub) ReadBatch(func() any) ([]any, error) {
	batch := s.batch
	s.batch = nil
	return batch, nil
}

func (s *memoryPipelineReaderStub) Close() error {
	return nil
}

func TestMemoryValueConvertedOnceAfterAggregation(t *testing.T) {
	const pageSize = int64(4096)

	tests := []struct {
		name string
		mode profiling.MemoryMode
		raw  int64
		want int64
	}{
		{name: "physical alloc page", mode: profiling.MemoryModePhysicalAlloc, raw: 1, want: pageSize},
		{name: "physical usage freed page", mode: profiling.MemoryModePhysicalUsage, raw: -1, want: -pageSize},
		{name: "virtual alloc bytes", mode: profiling.MemoryModeVirtualAlloc, raw: 1234, want: 1234},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &memoryPipelineBPFStub{state: [3]uint64{0, 1, 0}}
			reader := &memoryPipelineReaderStub{batch: []any{
				&abi.ProfilerEventBase{
					PIDTGID:   uint64(123) << 32,
					Kernstack: 0,
					Userstack: -1,
					Value:     tt.raw,
				},
			}}
			ringCtx := &ringBufferContext{
				bpf:                b,
				readerA:            reader,
				transferStateMapID: 1,
				stackMapAID:        2,
			}
			profiler := &memNativeProfiler{
				internalMode: tt.mode,
				probability:  100,
				pageSize:     pageSize,
			}

			counts, ring, err := ringCtx.drainFrozenRingBuffer(
				func() any { return &abi.ProfilerEventBase{} },
			)
			if err != nil {
				t.Fatalf("drainFrozenRingBuffer() error = %v", err)
			}

			var samples []*stackSample
			ringCtx.aggregateStacksAndEnqueue(counts, ring, func(record any) {
				samples = append(samples, record.(*stackSample))
			}, profiler.convertValueToBytes)

			if len(samples) != 1 {
				t.Fatalf("sample count = %d, want 1", len(samples))
			}
			if got := samples[0].Value; got != tt.want {
				t.Fatalf("sample value = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestNewBpfLoadConfigAttachOpts(t *testing.T) {
	tests := []struct {
		name            string
		mode            profiling.MemoryMode
		available       map[string]bool
		wantObject      string
		wantAttach      []bpf.AttachOption
		wantConstants   map[string]any
		wantProbability bool
	}{
		{
			name:       "virtual alloc",
			mode:       profiling.MemoryModeVirtualAlloc,
			wantObject: "native_virtual_alloc.o",
			wantAttach: []bpf.AttachOption{
				{ProgramName: "trace_mmap", Symbol: "do_mmap"},
			},
		},
		{
			name: "physical usage",
			mode: profiling.MemoryModePhysicalUsage,
			available: map[string]bool{
				symbolPageAddNewAnonRmap: true,
				symbolPageRemoveRmap:     true,
			},
			wantObject:      "native_physical_usage.o",
			wantProbability: true,
			wantAttach: []bpf.AttachOption{
				{ProgramName: programTracePageAlloc, Symbol: symbolPageAddNewAnonRmap},
				{ProgramName: programTracePageFree, Symbol: symbolPageRemoveRmap},
			},
			wantConstants: map[string]any{
				"profiler_folio_npages": false,
			},
		},
		{
			name: "physical usage folio",
			mode: profiling.MemoryModePhysicalUsage,
			available: map[string]bool{
				symbolFolioAddNewAnonRmap: true,
				symbolFolioRemoveRmapPtes: true,
			},
			wantObject:      "native_physical_usage.o",
			wantProbability: true,
			wantAttach: []bpf.AttachOption{
				{ProgramName: programTracePageAlloc, Symbol: symbolFolioAddNewAnonRmap},
				{ProgramName: programTracePageFree, Symbol: symbolFolioRemoveRmapPtes},
			},
			wantConstants: map[string]any{
				"profiler_folio_npages": true,
			},
		},
		{
			name: "physical alloc",
			mode: profiling.MemoryModePhysicalAlloc,
			available: map[string]bool{
				symbolPageAddNewAnonRmap: true,
			},
			wantObject:      "native_physical_alloc.o",
			wantProbability: true,
			wantAttach: []bpf.AttachOption{
				{ProgramName: programTracePageAlloc, Symbol: symbolPageAddNewAnonRmap},
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			restore := stubHasKprobeFunction(func(name string) bool {
				return tc.available[name]
			})
			defer restore()

			cfg, err := newNativeMemoryBPFLoadConfig(tc.mode, 123, 456, true, 42)
			if err != nil {
				t.Fatalf("newNativeMemoryBPFLoadConfig() error = %v", err)
			}

			if cfg.ObjectFile != tc.wantObject {
				t.Fatalf("ObjectFile = %q, want %q", cfg.ObjectFile, tc.wantObject)
			}

			if !reflect.DeepEqual(cfg.AttachOpts, tc.wantAttach) {
				t.Fatalf("AttachOpts = %#v, want %#v", cfg.AttachOpts, tc.wantAttach)
			}

			probability, ok := cfg.Constants["profiler_sampling_prob"]
			if ok != tc.wantProbability {
				t.Fatalf("profiler_sampling_prob present = %t, want %t", ok, tc.wantProbability)
			}
			if tc.wantProbability && probability != uint8(42) {
				t.Fatalf("profiler_sampling_prob = %v, want 42", probability)
			}
			for key, want := range tc.wantConstants {
				if got := cfg.Constants[key]; !reflect.DeepEqual(got, want) {
					t.Fatalf("Constants[%q] = %#v, want %#v", key, got, want)
				}
			}
			if tc.mode == profiling.MemoryModePhysicalUsage {
				for _, key := range []string{
					"profiler_alloc_reads_folio_nr_pages",
					"profiler_free_has_nr_pages",
				} {
					if _, ok := cfg.Constants[key]; ok {
						t.Fatalf("unexpected obsolete constant %q", key)
					}
				}
			}
		})
	}
}

func TestNewBpfLoadConfigThreadFilter(t *testing.T) {
	tests := []struct {
		name        string
		threadGroup bool
	}{
		{name: "target thread"},
		{name: "thread group", threadGroup: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := newNativeMemoryBPFLoadConfig(profiling.MemoryModeVirtualAlloc, 123, 0, tt.threadGroup, 100)
			if err != nil {
				t.Fatalf("newNativeMemoryBPFLoadConfig() error = %v", err)
			}
			if got := cfg.Constants["profiler_filter_threads"]; got != tt.threadGroup {
				t.Fatalf("profiler_filter_threads = %v, want %v", got, tt.threadGroup)
			}
		})
	}
}

func TestNewPhysicalAllocAttachOption(t *testing.T) {
	tests := []struct {
		name      string
		available map[string]bool
		want      bpf.AttachOption
		wantErr   bool
	}{
		{
			name: "page rmap",
			available: map[string]bool{
				symbolPageAddNewAnonRmap: true,
			},
			want: bpf.AttachOption{ProgramName: programTracePageAlloc, Symbol: symbolPageAddNewAnonRmap},
		},
		{
			name: "folio rmap",
			available: map[string]bool{
				symbolFolioAddNewAnonRmap: true,
			},
			want: bpf.AttachOption{ProgramName: programTracePageAlloc, Symbol: symbolFolioAddNewAnonRmap},
		},
		{
			name: "mixed rmap",
			available: map[string]bool{
				symbolPageAddNewAnonRmap:  true,
				symbolFolioAddNewAnonRmap: true,
			},
			want: bpf.AttachOption{ProgramName: programTracePageAlloc, Symbol: symbolFolioAddNewAnonRmap},
		},
		{
			name:    "missing hooks",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			restore := stubHasKprobeFunction(func(name string) bool {
				return tc.available[name]
			})
			defer restore()

			got, err := newPhysicalAllocAttachOption()
			if tc.wantErr {
				if err == nil {
					t.Fatal("newPhysicalAllocAttachOption() error = nil, want non-nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("newPhysicalAllocAttachOption() error = %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("newPhysicalAllocAttachOption() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestNewPhysicalUsageAttachConfig(t *testing.T) {
	tests := []struct {
		name      string
		available map[string]bool
		want      physicalUsageAttachConfig
		wantErr   bool
	}{
		{
			name: "page rmap pair",
			available: map[string]bool{
				symbolPageAddNewAnonRmap: true,
				symbolPageRemoveRmap:     true,
			},
			want: physicalUsageAttachConfig{
				AttachOpts: []bpf.AttachOption{
					{ProgramName: programTracePageAlloc, Symbol: symbolPageAddNewAnonRmap},
					{ProgramName: programTracePageFree, Symbol: symbolPageRemoveRmap},
				},
			},
		},
		{
			name: "folio rmap pair",
			available: map[string]bool{
				symbolFolioAddNewAnonRmap: true,
				symbolFolioRemoveRmapPtes: true,
			},
			want: physicalUsageAttachConfig{
				AttachOpts: []bpf.AttachOption{
					{ProgramName: programTracePageAlloc, Symbol: symbolFolioAddNewAnonRmap},
					{ProgramName: programTracePageFree, Symbol: symbolFolioRemoveRmapPtes},
				},
				CountFolioPages: true,
			},
		},
		{
			name: "mixed folio alloc page free",
			available: map[string]bool{
				symbolFolioAddNewAnonRmap: true,
				symbolPageRemoveRmap:      true,
			},
			want: physicalUsageAttachConfig{
				AttachOpts: []bpf.AttachOption{
					{ProgramName: programTracePageAlloc, Symbol: symbolFolioAddNewAnonRmap},
					{ProgramName: programTracePageFree, Symbol: symbolPageRemoveRmap},
				},
			},
		},
		{
			name: "prefer folio rmap pair",
			available: map[string]bool{
				symbolFolioAddNewAnonRmap: true,
				symbolFolioRemoveRmapPtes: true,
				symbolPageRemoveRmap:      true,
			},
			want: physicalUsageAttachConfig{
				AttachOpts: []bpf.AttachOption{
					{ProgramName: programTracePageAlloc, Symbol: symbolFolioAddNewAnonRmap},
					{ProgramName: programTracePageFree, Symbol: symbolFolioRemoveRmapPtes},
				},
				CountFolioPages: true,
			},
		},
		{
			name: "prefer mixed rmap over page rmap",
			available: map[string]bool{
				symbolPageAddNewAnonRmap:  true,
				symbolFolioAddNewAnonRmap: true,
				symbolPageRemoveRmap:      true,
			},
			want: physicalUsageAttachConfig{
				AttachOpts: []bpf.AttachOption{
					{ProgramName: programTracePageAlloc, Symbol: symbolFolioAddNewAnonRmap},
					{ProgramName: programTracePageFree, Symbol: symbolPageRemoveRmap},
				},
			},
		},
		{
			name: "incomplete folio pair",
			available: map[string]bool{
				symbolFolioAddNewAnonRmap: true,
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			restore := stubHasKprobeFunction(func(name string) bool {
				return tc.available[name]
			})
			defer restore()

			got, err := newPhysicalUsageAttachConfig()
			if tc.wantErr {
				if err == nil {
					t.Fatal("newPhysicalUsageAttachConfig() error = nil, want non-nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("newPhysicalUsageAttachConfig() error = %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("newPhysicalUsageAttachConfig() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func stubHasKprobeFunction(fn func(string) bool) func() {
	old := hasKprobeFunction
	hasKprobeFunction = fn
	return func() {
		hasKprobeFunction = old
	}
}
