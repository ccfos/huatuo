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
)

func TestParseThermalTemperature(t *testing.T) {
	got, err := parseThermalTemperature([]byte("42500\n"))
	if err != nil {
		t.Fatalf("parseThermalTemperature() error = %v", err)
	}
	if got != 42.5 {
		t.Fatalf("parseThermalTemperature() = %v, want 42.5", got)
	}
}

func TestParseThermalTemperatureRejectsMalformedInput(t *testing.T) {
	if _, err := parseThermalTemperature([]byte("unknown")); err == nil {
		t.Fatal("parseThermalTemperature() error = nil")
	}
}

func TestThermalCollectorUpdate(t *testing.T) {
	root := t.TempDir()
	originalRoot := filepath.Dir(procfs.DefaultPath())
	procfs.RootPrefix(root)
	t.Cleanup(func() { procfs.RootPrefix(originalRoot) })

	zone := filepath.Join(root, "sys/class/thermal/thermal_zone7")
	if err := os.MkdirAll(zone, 0o755); err != nil {
		t.Fatalf("create thermal fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(zone, "type"), []byte("x86_pkg_temp\n"), 0o600); err != nil {
		t.Fatalf("write type fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(zone, "temp"), []byte("42500\n"), 0o600); err != nil {
		t.Fatalf("write temperature fixture: %v", err)
	}

	metrics, err := (&thermalCollector{}).Update()
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if len(metrics) != 1 || metrics[0].Value != 42.5 {
		t.Fatalf("Update() metrics = %v, want one metric with value 42.5", metrics)
	}
	labels := metrics[0].Labels()
	if labels["zone"] != "thermal_zone7" || labels["type"] != "x86_pkg_temp" {
		t.Fatalf("Update() labels = %v", labels)
	}
}
