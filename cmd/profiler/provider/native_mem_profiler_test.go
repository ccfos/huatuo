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
	"huatuo-bamai/pkg/profiling"
)

func TestNewBpfLoadConfigAttachOpts(t *testing.T) {
	restore := stubHasKprobeFunction(func(name string) bool {
		switch name {
		case symbolPageAddNewAnonRmap, symbolPageRemoveRmap:
			return true
		default:
			return false
		}
	})
	defer restore()

	tests := []struct {
		name       string
		mode       profiling.MemoryMode
		wantObject string
		wantAttach []bpf.AttachOption
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
			name:       "physical usage",
			mode:       profiling.MemoryModePhysicalUsage,
			wantObject: "native_physical_usage.o",
			wantAttach: []bpf.AttachOption{
				{ProgramName: programTracePageAlloc, Symbol: symbolPageAddNewAnonRmap},
				{ProgramName: programTracePageFree, Symbol: symbolPageRemoveRmap},
			},
		},
		{
			name:       "physical alloc",
			mode:       profiling.MemoryModePhysicalAlloc,
			wantObject: "native_physical_alloc.o",
			wantAttach: []bpf.AttachOption{
				{ProgramName: programTracePageAlloc, Symbol: symbolPageAddNewAnonRmap},
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
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
			name: "folio rmap fallback",
			available: map[string]bool{
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

func TestNewPhysicalUsageAttachOptions(t *testing.T) {
	tests := []struct {
		name      string
		available map[string]bool
		want      []bpf.AttachOption
		wantErr   bool
	}{
		{
			name: "page rmap pair",
			available: map[string]bool{
				symbolPageAddNewAnonRmap: true,
				symbolPageRemoveRmap:     true,
			},
			want: []bpf.AttachOption{
				{ProgramName: programTracePageAlloc, Symbol: symbolPageAddNewAnonRmap},
				{ProgramName: programTracePageFree, Symbol: symbolPageRemoveRmap},
			},
		},
		{
			name: "folio rmap pair fallback",
			available: map[string]bool{
				symbolFolioAddNewAnonRmap: true,
				symbolFolioRemoveRmapPtes: true,
			},
			want: []bpf.AttachOption{
				{ProgramName: programTracePageAlloc, Symbol: symbolFolioAddNewAnonRmap},
				{ProgramName: programTracePageFree, Symbol: symbolFolioRemoveRmapPtes},
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

			got, err := newPhysicalUsageAttachOptions()
			if tc.wantErr {
				if err == nil {
					t.Fatal("newPhysicalUsageAttachOptions() error = nil, want non-nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("newPhysicalUsageAttachOptions() error = %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("newPhysicalUsageAttachOptions() = %#v, want %#v", got, tc.want)
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
