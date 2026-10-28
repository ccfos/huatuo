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

package runtime_test

import (
	"maps"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/ccfos/huatuo/pkg/metric"
	"github.com/ccfos/huatuo/pkg/metric/runtime"
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
			expected:  []string{"go_goroutines", "process_start_time_seconds"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			runtime.RegisterCollector(reg, tt.namespace)
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
	runtime.RegisterCollector(reg, "huatuo")

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
	runtime.RegisterCollector(reg, "huatuo")

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
	runtime.RegisterCollector(reg, "test")

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

func TestRegisterCollectorInfo(t *testing.T) {
	tests := []struct {
		name       string
		namespace  string
		metricName string
		help       string
		labels     map[string]string
	}{
		{
			name:       "build information",
			namespace:  "huatuo_bamai",
			metricName: "build_info",
			help:       "Build information for the application.",
			labels: map[string]string{
				"name":           "huatuo-bamai",
				"version":        "2.3.0",
				"git_commit":     "0123456789abcdef",
				"git_tree_state": "dirty",
				"build_time":     "20261028120000",
				"go_version":     "go1.24.3",
				"compiler":       "gc",
				"platform":       "linux/amd64",
			},
		},
		{
			name:       "custom information without namespace",
			metricName: "config_info",
			help:       "Active configuration identity.",
			labels:     map[string]string{"source": "local", "revision": ""},
		},
		{
			name:       "information without labels",
			namespace:  "test",
			metricName: "feature_info",
			help:       "Enabled feature information.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			wantLabels := map[string]string{
				metric.LabelHost:   metric.DefaultHostname(),
				metric.LabelRegion: metric.DefaultRegion(),
			}
			maps.Copy(wantLabels, tt.labels)
			runtime.RegisterCollector(
				reg,
				tt.namespace,
				runtime.InfoMetric{
					Name:   tt.metricName,
					Help:   tt.help,
					Labels: tt.labels,
				},
			)
			// Registration must snapshot labels rather than retain the caller's map.
			for name := range tt.labels {
				tt.labels[name] = "changed after registration"
			}
			families, err := reg.Gather()
			if err != nil {
				t.Fatalf("Gather() returned error: %v", err)
			}
			wantName := tt.metricName
			if tt.namespace != "" {
				wantName = tt.namespace + "_" + wantName
			}
			var family *dto.MetricFamily
			for _, candidate := range families {
				if candidate.GetName() == wantName {
					family = candidate
					break
				}
			}
			if family == nil {
				t.Fatalf("expected %s to be registered", wantName)
			}
			if family.GetName() != wantName || family.GetHelp() != tt.help {
				t.Fatalf("metric descriptor = %s / %s, want %s / %s", family.GetName(), family.GetHelp(), wantName, tt.help)
			}
			if family.GetType() != dto.MetricType_GAUGE || len(family.Metric) != 1 {
				t.Fatalf("expected one information gauge, got %v", family)
			}
			sample := family.Metric[0]
			if sample.GetGauge().GetValue() != 1 {
				t.Errorf("information gauge value = %v, want 1", sample.GetGauge().GetValue())
			}
			labels := make(map[string]string, len(sample.Label))
			for _, label := range sample.Label {
				labels[label.GetName()] = label.GetValue()
			}
			if !maps.Equal(labels, wantLabels) {
				t.Errorf("information labels = %v, want %v", labels, wantLabels)
			}
		})
	}
}

func TestRegisterCollectorMultipleInfo(t *testing.T) {
	reg := prometheus.NewRegistry()
	infos := []runtime.InfoMetric{
		{
			Name:   "build_info",
			Help:   "Build information.",
			Labels: map[string]string{"version": "1.2.3"},
		},
		{
			Name:   "config_info",
			Help:   "Configuration information.",
			Labels: map[string]string{"source": "local"},
		},
	}
	runtime.RegisterCollector(reg, "test", infos...)
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range infos {
		var found bool
		for _, family := range families {
			if family.GetName() != "test_"+info.Name {
				continue
			}
			found = true
			if len(family.Metric) != 1 || family.Metric[0].GetGauge().GetValue() != 1 {
				t.Errorf("expected one %s gauge fixed at 1, got %v", info.Name, family)
			}
		}
		if !found {
			t.Errorf("information metric %s missing", info.Name)
		}
	}
}
