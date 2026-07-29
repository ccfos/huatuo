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

package autotracing

import (
	"strings"
	"testing"
)

func TestValidateCPUConfig(t *testing.T) {
	t.Parallel()

	valid := cpuTracingConfig{
		intervalSeconds:         10,
		minTraceIntervalSeconds: 1800,
		perfDurationSeconds:     10,
		systemThreshold:         45,
		systemDeltaThreshold:    20,
	}
	tests := []struct {
		name      string
		modify    func(*cpuTracingConfig)
		wantError string
	}{
		{name: "valid"},
		{
			name:      "zero interval",
			modify:    func(c *cpuTracingConfig) { c.intervalSeconds = 0 },
			wantError: "sampling interval: timer duration must be positive",
		},
		{
			name: "interval overflow",
			modify: func(c *cpuTracingConfig) {
				c.intervalSeconds = maxTimerDurationSeconds + 1
			},
			wantError: "sampling interval: timer duration must not exceed",
		},
		{
			name: "zero minimum trace interval",
			modify: func(c *cpuTracingConfig) {
				c.minTraceIntervalSeconds = 0
			},
			wantError: "minimum trace interval: timer duration must be positive",
		},
		{
			name: "minimum trace interval overflow",
			modify: func(c *cpuTracingConfig) {
				c.minTraceIntervalSeconds = maxTimerDurationSeconds + 1
			},
			wantError: "minimum trace interval: timer duration must not exceed",
		},
		{
			name:      "zero perf duration",
			modify:    func(c *cpuTracingConfig) { c.perfDurationSeconds = 0 },
			wantError: "perf duration must be positive",
		},
		{
			name: "perf duration overflow",
			modify: func(c *cpuTracingConfig) {
				c.perfDurationSeconds = maxPerfDurationSeconds + 1
			},
			wantError: "perf duration must not exceed",
		},
		{
			name:      "negative system threshold",
			modify:    func(c *cpuTracingConfig) { c.systemThreshold = -1 },
			wantError: "system threshold: cpu percentage must be between 0 and 100",
		},
		{
			name: "system delta threshold above maximum",
			modify: func(c *cpuTracingConfig) {
				c.systemDeltaThreshold = 101
			},
			wantError: "system delta threshold: cpu percentage must be between 0 and 100",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := valid
			if tt.modify != nil {
				tt.modify(&config)
			}

			err := validateCPUConfig(config)
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("validateCPUConfig() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("validateCPUConfig() error = %v, want contain %q", err, tt.wantError)
			}
		})
	}
}
