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
	"testing"

	"github.com/ccfos/huatuo/pkg/profiling"
)

func TestNativeProfilersRejectMultiplePIDs(t *testing.T) {
	t.Parallel()

	cfg := &Config{PIDs: []int{123, 456}}
	tests := []struct {
		name      string
		start     func(context.Context) error
		wantError string
	}{
		{
			name:      "CPU",
			start:     (&cpuNativeProfiler{config: *cfg}).Start,
			wantError: "start native CPU profiler: multiple PIDs are not supported",
		},
		{
			name:      "memory",
			start:     (&memNativeProfiler{config: *cfg}).Start,
			wantError: "start native memory profiler: multiple PIDs are not supported",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.start(t.Context())
			if err == nil || err.Error() != tt.wantError {
				t.Fatalf("Start() error=%v, want %q", err, tt.wantError)
			}
		})
	}
}

func TestJavaProfilerCapturesConfiguration(t *testing.T) {
	cfg := &Config{
		Type:        profiling.TypeCPU,
		Freq:        100,
		ContainerID: "original-container",
	}
	task, err := New(profiling.ImplementationJava, cfg)
	if err != nil {
		t.Fatal(err)
	}

	cfg.Freq = 1
	cfg.ContainerID = "changed-container"
	if task.AggregationConfig.ContainerID != "original-container" || task.Impl.(*cpuJavaProfiler).config.Freq != 100 {
		t.Fatal("task aliases configuration")
	}
}
