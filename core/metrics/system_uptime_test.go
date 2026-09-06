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

func TestParseUptime(t *testing.T) {
	got, err := parseUptime("1234.56 789.01\n")
	if err != nil {
		t.Fatalf("parseUptime() error = %v", err)
	}
	if got != 1234.56 {
		t.Fatalf("parseUptime() = %v, want 1234.56", got)
	}
}

func TestParseUptimeRejectsMalformedInput(t *testing.T) {
	for _, raw := range []string{"", "12", "-1 2", "invalid 2", "1 invalid", "1 2 3"} {
		if _, err := parseUptime(raw); err == nil {
			t.Errorf("parseUptime(%q) error = nil", raw)
		}
	}
}

func TestUptimeCollectorUpdate(t *testing.T) {
	root := t.TempDir()
	originalRoot := filepath.Dir(procfs.DefaultPath())
	procfs.RootPrefix(root)
	t.Cleanup(func() { procfs.RootPrefix(originalRoot) })

	dir := filepath.Join(root, "proc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create proc fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "uptime"), []byte("1234.5 678.9\n"), 0o600); err != nil {
		t.Fatalf("write uptime fixture: %v", err)
	}

	metrics, err := (&uptimeCollector{}).Update()
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if len(metrics) != 1 || metrics[0].Value != 1234.5 {
		t.Fatalf("Update() metrics = %v, want one metric with value 1234.5", metrics)
	}
}
