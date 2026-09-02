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

package collector

import (
	"errors"
	"testing"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/types"
)

func TestNewBPFProgRuntimeCollectorDisabled(t *testing.T) {
	originalConfig := configSnapshot()
	t.Cleanup(func() { Set(originalConfig) })
	Set(&Config{})

	if _, err := newBPFProgRuntimeCollector(); !errors.Is(err, types.ErrNotSupported) {
		t.Fatalf("newBPFProgRuntimeCollector() error = %v, want ErrNotSupported", err)
	}
}

func TestBPFProgRuntimeCollectorUpdate(t *testing.T) {
	originalRead := readProgRuntimeData
	readProgRuntimeData = func() []bpf.ProgRuntimeMetric {
		return []bpf.ProgRuntimeMetric{{
			ProgramName:    "target",
			RunTimeNS:      2_500_000_000,
			RunCount:       7,
			Up:             true,
			AttachFailures: 3,
		}}
	}
	t.Cleanup(func() { readProgRuntimeData = originalRead })

	data, err := (&bpfProgRuntimeCollector{}).Update()
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if got := len(data); got < 4 || got > 5 {
		t.Fatalf("metric count = %d, want 4 profile metrics and optional online_cores", got)
	}

	byName := make(map[string]*metric.Data, 4)
	for _, item := range data {
		if item.Name() == "online_cores" {
			if item.Type() != metric.MetricTypeGauge || item.Value < 1 {
				t.Fatalf("online_cores = type %d, value %v; want positive gauge", item.Type(), item.Value)
			}
			continue
		}
		if _, exists := byName[item.Name()]; exists {
			t.Fatalf("duplicate metric %q", item.Name())
		}
		byName[item.Name()] = item
	}

	tests := []struct {
		name  string
		value float64
		typ   int
	}{
		{name: "up", value: 1, typ: metric.MetricTypeGauge},
		{name: "attach_failures_total", value: 3, typ: metric.MetricTypeCounter},
		{name: "runs_total", value: 7, typ: metric.MetricTypeCounter},
		{name: "runtime_seconds_total", value: 2.5, typ: metric.MetricTypeCounter},
	}
	for _, tt := range tests {
		item, ok := byName[tt.name]
		if !ok {
			t.Errorf("metric %q is missing", tt.name)
			continue
		}
		if item.Value != tt.value || item.Type() != tt.typ {
			t.Errorf("%s = value %v, type %d; want value %v, type %d",
				tt.name, item.Value, item.Type(), tt.value, tt.typ)
		}
		if got := item.Labels()["program"]; got != "target" {
			t.Errorf("%s program label = %q, want target", tt.name, got)
		}
		if item.Help() == "" {
			t.Errorf("%s help is empty", tt.name)
		}
	}
}

func BenchmarkBPFProgRuntimeCollectorUpdate(b *testing.B) {
	originalRead := readProgRuntimeData
	readProgRuntimeData = func() []bpf.ProgRuntimeMetric {
		return []bpf.ProgRuntimeMetric{{ProgramName: "target", Up: true}}
	}
	b.Cleanup(func() { readProgRuntimeData = originalRead })

	collector := &bpfProgRuntimeCollector{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := collector.Update(); err != nil {
			b.Fatal(err)
		}
	}
}
