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

package context

import "testing"

func TestParsePIDs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     string
		want      []int
		wantError bool
	}{
		{name: "empty"},
		{name: "single PID", value: "123", want: []int{123}},
		{name: "multiple PIDs", value: "123,456", want: []int{123, 456}},
		{name: "spaces", value: "123, 456", want: []int{123, 456}},
		{name: "empty item", value: "123,,456", wantError: true},
		{name: "non-numeric", value: "123,abc", wantError: true},
		{name: "zero", value: "0", wantError: true},
		{name: "negative", value: "-1", wantError: true},
		{name: "duplicate", value: "123,123", wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParsePIDs(tt.value)
			if tt.wantError {
				if err == nil {
					t.Fatalf("ParsePIDs(%q) error=nil, want error", tt.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParsePIDs(%q) error=%v", tt.value, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("ParsePIDs(%q)=%v, want %v", tt.value, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("ParsePIDs(%q)=%v, want %v", tt.value, got, tt.want)
				}
			}
		})
	}
}

func TestProfilerContextPID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		pids []int
		want int
	}{
		{name: "empty"},
		{name: "single", pids: []int{123}, want: 123},
		{name: "multiple returns first", pids: []int{123, 456}, want: 123},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pctx := &ProfilerContext{PIDs: tt.pids}
			if got := pctx.PID(); got != tt.want {
				t.Fatalf("PID()=%d, want %d", got, tt.want)
			}
		})
	}
}
