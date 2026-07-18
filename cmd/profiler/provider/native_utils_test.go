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

import "testing"

func TestNewNativeBPFConstants(t *testing.T) {
	tests := []struct {
		name        string
		threadGroup bool
	}{
		{name: "target thread"},
		{name: "thread group", threadGroup: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			constants := newNativeBPFConstants(123, 456, tt.threadGroup)
			if got := constants["profiler_filter_pid"]; got != uint32(123) {
				t.Fatalf("profiler_filter_pid = %v, want 123", got)
			}
			if got := constants["profiler_filter_css"]; got != uint64(456) {
				t.Fatalf("profiler_filter_css = %v, want 456", got)
			}
			if got := constants["profiler_filter_threads"]; got != tt.threadGroup {
				t.Fatalf("profiler_filter_threads = %v, want %v", got, tt.threadGroup)
			}
		})
	}
}

func TestNewNativeBPFConstantsReturnsIndependentMaps(t *testing.T) {
	first := newNativeBPFConstants(1, 2, false)
	second := newNativeBPFConstants(1, 2, false)
	first["profiler_filter_pid"] = uint32(3)

	if got := second["profiler_filter_pid"]; got != uint32(1) {
		t.Fatalf("second profiler_filter_pid = %v, want 1", got)
	}
}
