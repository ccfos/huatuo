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
	"testing"

	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/output"
)

func TestNewJavaAggregatorUsesOneShotAggregation(t *testing.T) {
	t.Parallel()

	pctx := &pcontext.ProfilerContext{OutputFormat: output.FormatCollapsed}
	if _, err := newJavaAggregator(pctx); err != nil {
		t.Fatalf("newJavaAggregator() error=%v", err)
	}
	if !pctx.IsOneShotAgg {
		t.Fatal("newJavaAggregator() did not enable one-shot aggregation")
	}
}

func TestValidateJavaToolLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		pids      []int
		toolLimit int
		wantError string
	}{
		{name: "zero disables limit", pids: []int{1, 2}, toolLimit: 0},
		{name: "negative disables limit", pids: []int{1, 2}, toolLimit: -1},
		{name: "equal to limit", pids: []int{1, 2}, toolLimit: 2},
		{
			name:      "exceeds limit",
			pids:      []int{1, 2, 3},
			toolLimit: 2,
			wantError: "start Java profiler: too many target processes: limit=2, found=3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateJavaToolLimit(tt.pids, tt.toolLimit)
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("validateJavaToolLimit() error=%v, want nil", err)
				}
				return
			}
			if err == nil || err.Error() != tt.wantError {
				t.Fatalf("validateJavaToolLimit() error=%v, want %q", err, tt.wantError)
			}
		})
	}
}

func TestNativeProfilersRejectMultiplePIDs(t *testing.T) {
	t.Parallel()

	pctx := &pcontext.ProfilerContext{PIDs: []int{123, 456}}
	tests := []struct {
		name      string
		start     func(*pcontext.ProfilerContext) error
		wantError string
	}{
		{
			name:      "CPU",
			start:     (&cpuNativeProfiler{}).Start,
			wantError: "start native CPU profiler: multiple PIDs are not supported",
		},
		{
			name:      "memory",
			start:     (&memNativeProfiler{}).Start,
			wantError: "start native memory profiler: multiple PIDs are not supported",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.start(pctx)
			if err == nil || err.Error() != tt.wantError {
				t.Fatalf("Start() error=%v, want %q", err, tt.wantError)
			}
		})
	}
}
