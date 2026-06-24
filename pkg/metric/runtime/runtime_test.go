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

package runtime

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestRegisterCollector(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
		expected  []string
	}{
		{
			name:      "register with huatuo namespace",
			namespace: "huatuo",
			expected:  []string{"huatuo_go_goroutines", "huatuo_process_start_time_seconds"},
		},
		{
			name:      "register with null namespace",
			namespace: "",
			expected:  []string{"_go_goroutines", "process_start_time_seconds"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			RegisterCollector(reg, tt.namespace)
			families, err := reg.Gather()
			if err != nil {
				t.Fatalf("Gather() returned error: %v", err)
			}
			metricSet := make(map[string]struct{}, len(families))
			for _, f := range families {
				metricSet[f.GetName()] = struct{}{}
			}
			for _, exp := range tt.expected {
				if _, ok := metricSet[exp]; !ok {
					t.Errorf("expected metric %q to be registered", exp)
				}
			}
		})
	}
}

func TestRegisterCollectorProcessMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	RegisterCollector(reg, "huatuo")

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather() returned error: %v", err)
	}

	hasProcessCPU := false
	hasProcessOpenFDs := false
	for _, f := range families {
		name := f.GetName()
		if name == "huatuo_process_cpu_seconds_total" {
			hasProcessCPU = true
		}
		if name == "huatuo_process_open_fds" {
			hasProcessOpenFDs = true
		}
	}

	if !hasProcessCPU {
		t.Errorf("expected process_cpu_seconds_total metric with namespace prefix")
	}
	if !hasProcessOpenFDs {
		t.Errorf("expected process_open_fds metric with namespace prefix")
	}
}

func TestRegisterCollectorGoMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	RegisterCollector(reg, "huatuo")

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather() returned error: %v", err)
	}

	hasGoGoroutines := false
	hasGoThreads := false
	for _, f := range families {
		name := f.GetName()
		if name == "huatuo_go_goroutines" {
			hasGoGoroutines = true
		}
		if name == "huatuo_go_threads" {
			hasGoThreads = true
		}
	}

	if !hasGoGoroutines {
		t.Errorf("expected go_goroutines metric with namespace prefix")
	}
	if !hasGoThreads {
		t.Errorf("expected go_threads metric with namespace prefix")
	}
}

func TestRegisterCollectorNoDuplicate(t *testing.T) {
	reg := prometheus.NewRegistry()
	RegisterCollector(reg, "test")

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather() returned error: %v", err)
	}

	seen := make(map[string]int)
	for _, f := range families {
		name := f.GetName()
		seen[name]++
	}

	for name, count := range seen {
		if count > 1 {
			t.Errorf("metric %q registered %d times, want 1", name, count)
		}
	}
}
