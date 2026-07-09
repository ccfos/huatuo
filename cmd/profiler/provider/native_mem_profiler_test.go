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
	t.Parallel()

	tests := []struct {
		name       string
		mode       string
		wantObject string
		wantAttach []bpf.AttachOption
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
			name:       "physical usage",
			mode:       modePhysicalUsage,
			wantObject: "native_physical_usage.o",
			wantAttach: []bpf.AttachOption{
				{ProgramName: "trace_page_alloc", Symbol: "page_add_new_anon_rmap"},
				{ProgramName: "trace_page_free", Symbol: "page_remove_rmap"},
			},
		},
		{
			name:       "physical alloc",
			mode:       modePhysicalAlloc,
			wantObject: "native_physical_alloc.o",
			wantAttach: []bpf.AttachOption{
				{ProgramName: "trace_page_alloc", Symbol: "page_add_new_anon_rmap"},
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

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
