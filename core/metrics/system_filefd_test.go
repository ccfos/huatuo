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

func TestParseFileFD(t *testing.T) {
	allocated, maximum, err := parseFileFD("128\t0\t4096\n")
	if err != nil {
		t.Fatalf("parseFileFD() error = %v", err)
	}
	if allocated != 128 || maximum != 4096 {
		t.Fatalf("parseFileFD() = (%d, %d), want (128, 4096)", allocated, maximum)
	}
}

func TestParseFileFDRejectsMalformedInput(t *testing.T) {
	for _, raw := range []string{"", "1 2", "bad 0 100", "1 0 bad"} {
		if _, _, err := parseFileFD(raw); err == nil {
			t.Errorf("parseFileFD(%q) error = nil", raw)
		}
	}
}

func TestFileFDCollectorUpdate(t *testing.T) {
	root := t.TempDir()
	originalRoot := filepath.Dir(procfs.DefaultPath())
	procfs.RootPrefix(root)
	t.Cleanup(func() { procfs.RootPrefix(originalRoot) })

	dir := filepath.Join(root, "proc/sys/fs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create proc fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file-nr"), []byte("128 0 4096\n"), 0o600); err != nil {
		t.Fatalf("write file-nr fixture: %v", err)
	}

	metrics, err := (&fileFDCollector{}).Update()
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	want := []float64{128, 4096, 3.125}
	for i := range want {
		if metrics[i].Value != want[i] {
			t.Errorf("metric %d value = %v, want %v", i, metrics[i].Value, want[i])
		}
	}
}
