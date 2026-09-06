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

func TestParseEntropyValue(t *testing.T) {
	got, err := parseEntropyValue("entropy_avail", []byte("256\n"))
	if err != nil {
		t.Fatalf("parseEntropyValue() error = %v", err)
	}
	if got != 256 {
		t.Fatalf("parseEntropyValue() = %d, want 256", got)
	}
}

func TestParseEntropyValueRejectsMalformedInput(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte("-1"), []byte("unknown")} {
		if _, err := parseEntropyValue("entropy_avail", raw); err == nil {
			t.Errorf("parseEntropyValue(%q) error = nil", raw)
		}
	}
}

func TestEntropyCollectorUpdate(t *testing.T) {
	root := t.TempDir()
	originalRoot := filepath.Dir(procfs.DefaultPath())
	procfs.RootPrefix(root)
	t.Cleanup(func() { procfs.RootPrefix(originalRoot) })

	dir := filepath.Join(root, "proc/sys/kernel/random")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create proc fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "entropy_avail"), []byte("128\n"), 0o600); err != nil {
		t.Fatalf("write entropy fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "poolsize"), []byte("256\n"), 0o600); err != nil {
		t.Fatalf("write poolsize fixture: %v", err)
	}

	metrics, err := (&entropyCollector{}).Update()
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	want := []float64{128, 256, 50}
	for i := range want {
		if metrics[i].Value != want[i] {
			t.Errorf("metric %d value = %v, want %v", i, metrics[i].Value, want[i])
		}
	}
}
