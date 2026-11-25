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
	"math"
	"reflect"
	"testing"

	"github.com/cilium/ebpf"

	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/profiling/aggregation"
	"github.com/ccfos/huatuo/internal/profiling/bpfmap"

	"github.com/ccfos/huatuo/internal/profiling/pipeline"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/pkg/profiling"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestNativeMemoryReadDataLoopRequiresStart(t *testing.T) {
	err := (&memNativeProfiler{}).ReadDataLoop(t.Context(), func(*pipeline.SampleBatch) error { return nil })
	if err == nil || err.Error() != "native memory event readers are not initialized; call Start before ReadDataLoop" {
		t.Fatalf("ReadDataLoop() error = %v, want uninitialized-reader error", err)
	}
}

type memoryPipelineBPFStub struct {
	bpf.BPF
	state [3]uint64
}

func (s *memoryPipelineBPFStub) ReadMap(mapID uint32, key []byte) ([]byte, error) {
	if mapID != 1 {
		return nil, ebpf.ErrKeyNotExist
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
	batch bpf.PerfEventBatch
}

func (s *memoryPipelineReaderStub) ReadInto(any) error {
	return nil
}

func (s *memoryPipelineReaderStub) ReadBatch(func() any, ...bpf.ReadBatchOption) (bpf.PerfEventBatch, error) {
	batch := s.batch
	s.batch = bpf.PerfEventBatch{}

	return batch, nil
}

func (s *memoryPipelineReaderStub) Close() error {
	return nil
}

func TestMemorySamplesUseBytes(t *testing.T) {
	const pageSize = int64(4096)

	tests := []struct {
		name string
		mode profiling.Mode
		raw  int64
		want int64
	}{
		{
			name: "physical alloc page",
			mode: profiling.ModePhysicalAlloc,
			raw:  1,
			want: pageSize,
		},
		{
			name: "physical usage freed page",
			mode: profiling.ModePhysicalUsage,
			raw:  -1,
			want: -pageSize,
		},
		{
			name: "virtual alloc bytes",
			mode: profiling.ModeVirtualAlloc,
			raw:  1234,
			want: 1234,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &memoryPipelineBPFStub{state: [3]uint64{0, 1, 0}}
			reader := &memoryPipelineReaderStub{
				batch: bpf.PerfEventBatch{
					Events: []any{
						&abi.ProfilerEventBase{
							PIDTGID:   uint64(123) << 32,
							Kernstack: 0,
							Userstack: -1,
							Value:     tt.raw,
						},
					},
				},
			}
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

			var samples []aggregation.Sample
			err := ringCtx.swapAndDrainRingBuffer(
				func() any { return &abi.ProfilerEventBase{} },
				func(batch *pipeline.SampleBatch) error { samples = append(samples, batch.Samples...); return nil },
				profiler.convertValueToBytes,
			)
			if err != nil {
				t.Fatal(err)
			}

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
		mode            profiling.Mode
		available       map[string]bool
		wantObject      string
		wantAttach      []bpf.AttachOption
		wantConstants   map[string]any
		wantProbability bool
	}{
		{
			name:       "virtual alloc",
			mode:       profiling.ModeVirtualAlloc,
			wantObject: "native_virtual_alloc.o",
			wantAttach: []bpf.AttachOption{
				{
					ProgramName: "trace_mmap",
					Symbol:      "do_mmap",
				},
			},
		},
		{
			name: "physical usage",
			mode: profiling.ModePhysicalUsage,
			available: map[string]bool{
				symbolPageAddNewAnonRmap: true,
				symbolPageRemoveRmap:     true,
			},
			wantObject:      "native_physical_usage.o",
			wantProbability: true,
			wantAttach: []bpf.AttachOption{
				{
					ProgramName: programTracePageAlloc,
					Symbol:      symbolPageAddNewAnonRmap,
				},
				{
					ProgramName: programTracePageFree,
					Symbol:      symbolPageRemoveRmap,
				},
			},
			wantConstants: map[string]any{
				"profiler_folio_npages": false,
			},
		},
		{
			name: "physical usage folio",
			mode: profiling.ModePhysicalUsage,
			available: map[string]bool{
				symbolFolioAddNewAnonRmap: true,
				symbolFolioRemoveRmapPtes: true,
			},
			wantObject:      "native_physical_usage.o",
			wantProbability: true,
			wantAttach: []bpf.AttachOption{
				{
					ProgramName: programTracePageAlloc,
					Symbol:      symbolFolioAddNewAnonRmap,
				},
				{
					ProgramName: programTracePageFree,
					Symbol:      symbolFolioRemoveRmapPtes,
				},
			},
			wantConstants: map[string]any{
				"profiler_folio_npages": true,
			},
		},
		{
			name: "physical alloc",
			mode: profiling.ModePhysicalAlloc,
			available: map[string]bool{
				symbolPageAddNewAnonRmap: true,
			},
			wantObject:      "native_physical_alloc.o",
			wantProbability: true,
			wantAttach: []bpf.AttachOption{
				{
					ProgramName: programTracePageAlloc,
					Symbol:      symbolPageAddNewAnonRmap,
				},
			},
			wantConstants: map[string]any{
				"profiler_folio_npages": false,
			},
		},
		{
			name: "physical alloc folio",
			mode: profiling.ModePhysicalAlloc,
			available: map[string]bool{
				symbolFolioAddNewAnonRmap: true,
			},
			wantObject:      "native_physical_alloc.o",
			wantProbability: true,
			wantAttach: []bpf.AttachOption{
				{
					ProgramName: programTracePageAlloc,
					Symbol:      symbolFolioAddNewAnonRmap,
				},
			},
			wantConstants: map[string]any{
				"profiler_folio_npages": true,
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

			cfg, err := newNativeMemoryBPFLoadConfig(
				tc.mode,
				123,
				pod.CgroupCSSRef{CSSAddr: 456, CgroupID: 789},
				true,
				42,
			)
			if err != nil {
				t.Fatalf("newNativeMemoryBPFLoadConfig() error = %v", err)
			}

			if cfg.ObjectFile != tc.wantObject {
				t.Fatalf("ObjectFile = %q, want %q", cfg.ObjectFile, tc.wantObject)
			}
			if got := cfg.Constants["profiler_filter_cgroup_id"]; got != uint64(789) {
				t.Fatalf("profiler_filter_cgroup_id = %v, want 789", got)
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
					t.Fatalf(
						"Constants[%q] = %#v, want %#v",
						key,
						got,
						want,
					)
				}
			}

			if tc.mode == profiling.ModePhysicalUsage {
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
		{
			name:        "thread group",
			threadGroup: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := newNativeMemoryBPFLoadConfig(
				profiling.ModeVirtualAlloc,
				123,
				pod.CgroupCSSRef{},
				tt.threadGroup,
				100,
			)
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
			want: bpf.AttachOption{
				ProgramName: programTracePageAlloc,
				Symbol:      symbolPageAddNewAnonRmap,
			},
		},
		{
			name: "folio rmap",
			available: map[string]bool{
				symbolFolioAddNewAnonRmap: true,
			},
			want: bpf.AttachOption{
				ProgramName: programTracePageAlloc,
				Symbol:      symbolFolioAddNewAnonRmap,
			},
		},
		{
			name: "mixed rmap",
			available: map[string]bool{
				symbolPageAddNewAnonRmap:  true,
				symbolFolioAddNewAnonRmap: true,
			},
			want: bpf.AttachOption{
				ProgramName: programTracePageAlloc,
				Symbol:      symbolFolioAddNewAnonRmap,
			},
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
					{
						ProgramName: programTracePageAlloc,
						Symbol:      symbolPageAddNewAnonRmap,
					},
					{
						ProgramName: programTracePageFree,
						Symbol:      symbolPageRemoveRmap,
					},
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
					{
						ProgramName: programTracePageAlloc,
						Symbol:      symbolFolioAddNewAnonRmap,
					},
					{
						ProgramName: programTracePageFree,
						Symbol:      symbolFolioRemoveRmapPtes,
					},
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
					{
						ProgramName: programTracePageAlloc,
						Symbol:      symbolFolioAddNewAnonRmap,
					},
					{
						ProgramName: programTracePageFree,
						Symbol:      symbolPageRemoveRmap,
					},
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
					{
						ProgramName: programTracePageAlloc,
						Symbol:      symbolFolioAddNewAnonRmap,
					},
					{
						ProgramName: programTracePageFree,
						Symbol:      symbolFolioRemoveRmapPtes,
					},
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
					{
						ProgramName: programTracePageAlloc,
						Symbol:      symbolFolioAddNewAnonRmap,
					},
					{
						ProgramName: programTracePageFree,
						Symbol:      symbolPageRemoveRmap,
					},
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

func TestPhysicalUsageDoesNotExtrapolateSamples(t *testing.T) {
	for _, probability := range []uint{1, 10, 100} {
		p := memNativeProfiler{
			internalMode: profiling.ModePhysicalUsage,
			pageSize:     4096,
			probability:  probability,
		}

		for _, pages := range []int64{-2, 0, 2} {
			got, err := p.convertValueToBytes(pages)
			if err != nil || got != pages*4096 {
				t.Fatalf("probability=%d pages=%d bytes=%d error=%v", probability, pages, got, err)
			}
		}
	}
}

func TestCompactRetainedEvents(t *testing.T) {
	e := func(pid uint64, stack int32, value int64) *abi.ProfilerEventBase {
		return &abi.ProfilerEventBase{
			PIDTGID:   pid,
			Userstack: stack,
			Value:     value,
		}
	}
	convert := func(v int64) (int64, error) { return v * 4096, nil }
	a, b, c := e(1, 2, 10), e(1, 2, -3), e(2, 2, 4)
	got, err := compactRetainedEvents([]any{a, b, c}, convert)
	if err != nil || len(got) != 2 || got[0].valueBytes != 7*4096 || got[1].valueBytes != 4*4096 {
		t.Fatalf("got=%v err=%v", got, err)
	}
	if a.Value != 10 || b.Value != -3 {
		t.Fatal("input mutated")
	}
	if got[0].source.Value != 0 || got[1].source.Value != 0 {
		t.Fatal("converted value leaked into raw source identity")
	}

	_, err = compactRetainedEvents([]any{e(1, 2, math.MaxInt64), e(1, 2, 1)}, func(v int64) (int64, error) { return v, nil })
	if err == nil {
		t.Fatal("overflow accepted")
	}
	_, err = compactRetainedEvents([]any{e(1, 2, math.MinInt64), e(1, 2, -1)}, func(v int64) (int64, error) { return v, nil })
	if err == nil {
		t.Fatal("underflow accepted")
	}
	want := errors.New("conversion")
	_, err = compactRetainedEvents([]any{a}, func(int64) (int64, error) { return 0, want })
	if !errors.Is(err, want) {
		t.Fatal(err)
	}
	_, err = compactRetainedEvents([]any{42}, convert)
	if err == nil {
		t.Fatal("invalid type accepted")
	}
}

func TestCompactRetainedSourceKeysAndScaling(t *testing.T) {
	base := abi.ProfilerEventBase{
		PIDTGID:   1,
		Userstack: -1,
		Kernstack: -1,
		Value:     3,
	}
	events := []any{&base}
	for i := 0; i < 4; i++ {
		e := base
		switch i {
		case 0:
			e.PIDTGID = 2
		case 1:
			e.Userstack = 2
		case 2:
			e.Kernstack = 2
		case 3:
			e.Comm[0] = 'x'
		}
		events = append(events, &e)
	}
	got, err := compactRetainedEvents(events, func(v int64) (int64, error) { return v / 2, nil })
	if err != nil || len(got) != 5 {
		t.Fatalf("len=%d err=%v", len(got), err)
	}
	got, err = compactRetainedEvents([]any{&base, &base}, func(v int64) (int64, error) { return v / 2, nil })
	if err != nil || got[0].valueBytes != 2 {
		t.Fatal("conversion must precede summing")
	}
}

func TestRetainedCompactionPreservesPerfAccounting(t *testing.T) {
	reader := &frozenRingReaderStub{batches: []bpf.PerfEventBatch{{
		Events: []any{&abi.ProfilerEventBase{
			PIDTGID:   1,
			Userstack: -1,
			Kernstack: -1,
			Value:     3,
		}, &abi.ProfilerEventBase{
			PIDTGID:   1,
			Userstack: -1,
			Kernstack: -1,
			Value:     -1,
		}},
		LostSamples: 1,
	}}}
	obj := &frozenRingBPFStub{values: map[uint32]uint64{bpfmap.SampleCountAIdx: 3}}
	r := &ringBufferContext{
		bpf:                obj,
		transferStateMapID: 1,
		mode:               profiling.ModePhysicalUsage,
	}
	var total int64
	var samples int
	var dropped uint64
	accounted, expected, err := r.drainRingBuffer(nativeRingBuffer{
		reader:         reader,
		sampleCountIdx: bpfmap.SampleCountAIdx,
	}, func() any { return &abi.ProfilerEventBase{} }, func(b *pipeline.SampleBatch) error {
		samples += len(b.Samples)
		for _, s := range b.Samples {
			total += s.Value
		}
		dropped += b.DroppedSamples["collection"]
		return nil
	}, func(v int64) (int64, error) { return v * 4096, nil })
	if err != nil || accounted != 3 || expected != 3 || samples != 1 || total != 8192 || dropped != 1 {
		t.Fatalf("err=%v accounted=%d expected=%d samples=%d total=%d lost=%d", err, accounted, expected, samples, total, dropped)
	}
}

// BenchmarkRetainedBurstDelivery exercises the production sample processor.
// Missing stack IDs isolate processing from symbol I/O; sources vary by PID.
func BenchmarkRetainedBurstDelivery(b *testing.B) {
	for _, sources := range []int{8, 32768} {
		events := make([]any, 32768)
		var expected int64
		for i := range events {
			value := int64(1)
			if i%4 == 0 {
				value = -1
			}
			events[i] = &abi.ProfilerEventBase{
				PIDTGID:   uint64(i%sources+1) << 32,
				Userstack: -1,
				Kernstack: -1,
				Value:     value,
			}
			expected += value * 4096
		}
		for _, compact := range []bool{false, true} {
			b.Run(fmt.Sprintf("sources=%d/compact=%t", sources, compact), func(b *testing.B) {
				mode := profiling.ModeVirtualAlloc
				if compact {
					mode = profiling.ModePhysicalUsage
				}
				b.ReportAllocs()
				for b.Loop() {
					processor := nativeSampleProcessor{
						mode:        mode,
						kernelCache: make(map[int32][]types.ProfileFrame),
						userCache:   make(map[userStackCacheKey][]types.ProfileFrame),
					}
					var total int64
					err := processor.enqueueSamples(events, func(v int64) (int64, error) { return v * 4096, nil }, func(batch *pipeline.SampleBatch) error {
						for i := range batch.Samples {
							total += batch.Samples[i].Value
						}
						return nil
					})
					if err != nil || total != expected {
						b.Fatalf("total=%d want=%d err=%v", total, expected, err)
					}
				}
			})
		}
	}
}
