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
	"os"
	"path/filepath"
	"testing"

	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/pkg/metric"

	procfslib "github.com/prometheus/procfs"
)

func TestPSIDataEmitsGaugesAndCounters(t *testing.T) {
	stats := procfslib.PSIStats{
		Some: &procfslib.PSILine{Avg10: 1.5, Avg60: 2.5, Avg300: 3.5, Total: 12_500_000},
		Full: &procfslib.PSILine{Avg10: 0.25, Avg60: 0.5, Avg300: 0.75, Total: 500_000},
	}

	data := psiData(nil, "memory", stats)
	if len(data) != 8 {
		t.Fatalf("psiData() emitted %d series, want 8 (some+full x avg10/60/300/total)", len(data))
	}

	byName := map[string]*metricDataProbe{}
	for _, d := range data {
		byName[d.Name()] = &metricDataProbe{value: d.Value, valueType: d.Type()}
	}

	checks := []struct {
		name  string
		value float64
		mtype int
	}{
		{"memory_some_avg10", 1.5, metric.MetricTypeGauge},
		{"memory_some_avg300", 3.5, metric.MetricTypeGauge},
		{"memory_some_seconds_total", 12.5, metric.MetricTypeCounter},
		{"memory_full_avg10", 0.25, metric.MetricTypeGauge},
		{"memory_full_seconds_total", 0.5, metric.MetricTypeCounter},
	}
	for _, c := range checks {
		got, ok := byName[c.name]
		if !ok {
			t.Errorf("missing series %q", c.name)
			continue
		}
		if got.value != c.value {
			t.Errorf("%s value=%v, want %v", c.name, got.value, c.value)
		}
		if got.valueType != c.mtype {
			t.Errorf("%s type=%d, want %d", c.name, got.valueType, c.mtype)
		}
	}
}

type metricDataProbe struct {
	value     float64
	valueType int
}

func TestPSICollectorHostData(t *testing.T) {
	root := t.TempDir()
	originalPrefix := filepath.Dir(procfs.DefaultPath())
	procfs.RootPrefix(root)
	defer procfs.RootPrefix(originalPrefix)

	pressureDir := filepath.Join(root, "proc", "pressure")
	if err := os.MkdirAll(pressureDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error=%v", err)
	}
	if err := os.WriteFile(filepath.Join(pressureDir, "cpu"),
		[]byte("some avg10=4.00 avg60=2.00 avg300=1.00 total=9000000\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(cpu) error=%v", err)
	}
	for _, resource := range []string{"memory", "io"} {
		if err := os.WriteFile(filepath.Join(pressureDir, resource),
			[]byte("some avg10=0.00 avg60=0.00 avg300=0.00 total=0\n"), 0o600); err != nil {
			t.Fatalf("WriteFile(%s) error=%v", resource, err)
		}
	}

	collector := &psiCollector{}
	data, err := collector.hostData()
	if err != nil {
		t.Fatalf("hostData() error=%v", err)
	}

	// cpu emits some only (4), memory and io emit some (4) each in this
	// fixture.
	if len(data) != 12 {
		t.Fatalf("hostData() emitted %d series, want 12", len(data))
	}

	for _, d := range data {
		if d.Name() == "cpu_some_avg10" && d.Value != 4.0 {
			t.Errorf("cpu_some_avg10=%v, want 4.0", d.Value)
		}
		if d.Name() == "cpu_some_seconds_total" && d.Value != 9.0 {
			t.Errorf("cpu_some_seconds_total=%v, want 9.0", d.Value)
		}
	}
}

// Smoke test against the real kernel when pressure files are available.
func TestPSICollectorRealFiles(t *testing.T) {
	if _, err := os.Stat(filepath.Join(procfs.DefaultPath(), "pressure", "cpu")); err != nil {
		t.Skip("kernel does not expose /proc/pressure")
	}

	collector := &psiCollector{}
	data, err := collector.hostData()
	if err != nil {
		t.Fatalf("hostData() error=%v", err)
	}
	if len(data) == 0 {
		t.Fatal("hostData() returned no series against the real kernel")
	}
}
