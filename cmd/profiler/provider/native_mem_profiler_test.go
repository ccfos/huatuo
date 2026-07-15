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
	"reflect"
	"testing"

	"huatuo-bamai/internal/bpf"
)

func TestNewBpfLoadConfigAttachOpts(t *testing.T) {
	tests := []struct {
		name          string
		mode          string
		available     map[string]bool
		wantObject    string
		wantAttach    []bpf.AttachOption
		wantConstants map[string]any
	}{
		{
			name:       "virtual alloc",
			mode:       modeVirtualAlloc,
			wantObject: "native_virtual_alloc.o",
			wantAttach: []bpf.AttachOption{
				{ProgramName: "trace_mmap", Symbol: "do_mmap"},
			},
		},
		{
			name: "physical usage",
			mode: modePhysicalUsage,
			available: map[string]bool{
				symbolPageAddNewAnonRmap: true,
				symbolPageRemoveRmap:     true,
			},
			wantObject: "native_physical_usage.o",
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
			mode: modePhysicalUsage,
			available: map[string]bool{
				symbolFolioAddNewAnonRmap: true,
				symbolFolioRemoveRmapPtes: true,
			},
			wantObject: "native_physical_usage.o",
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
			mode: modePhysicalAlloc,
			available: map[string]bool{
				symbolPageAddNewAnonRmap: true,
			},
			wantObject: "native_physical_alloc.o",
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

			cfg, err := newBpfLoadConfig(tc.mode, 123, 456, true, 42)
			if err != nil {
				t.Fatalf("newBpfLoadConfig() error = %v", err)
			}

			if cfg.ObjectFile != tc.wantObject {
				t.Fatalf("ObjectFile = %q, want %q", cfg.ObjectFile, tc.wantObject)
			}

			if !reflect.DeepEqual(cfg.AttachOpts, tc.wantAttach) {
				t.Fatalf("AttachOpts = %#v, want %#v", cfg.AttachOpts, tc.wantAttach)
			}
			for key, want := range tc.wantConstants {
				if got := cfg.Constants[key]; !reflect.DeepEqual(got, want) {
					t.Fatalf("Constants[%q] = %#v, want %#v", key, got, want)
				}
			}
			if tc.mode == modePhysicalUsage {
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
