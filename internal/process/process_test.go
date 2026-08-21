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

package process

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"huatuo-bamai/internal/procfs"
)

func TestExecutable(t *testing.T) {
	tmpRoot := setProcRoot(t)
	procPath := filepath.Join(tmpRoot, "proc", "100")
	if err := os.MkdirAll(procPath, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", procPath, err)
	}
	if err := os.Symlink("/usr/bin/python3", filepath.Join(procPath, "exe")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	executable, err := Executable(100)
	if err != nil {
		t.Fatalf("Executable(100) error = %v", err)
	}
	if executable != "/usr/bin/python3" {
		t.Fatalf("Executable(100) = %q, want %q", executable, "/usr/bin/python3")
	}
}

func TestExecutableMissing(t *testing.T) {
	tmpRoot := setProcRoot(t)
	procPath := filepath.Join(tmpRoot, "proc", "100")
	if err := os.MkdirAll(procPath, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", procPath, err)
	}

	if _, err := Executable(100); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Executable(100) error = %v, want fs.ErrNotExist", err)
	}
}

func TestPPID(t *testing.T) {
	tmpRoot := setProcRoot(t)
	procPath := filepath.Join(tmpRoot, "proc", "100")
	if err := os.MkdirAll(procPath, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", procPath, err)
	}

	stat := "100 (python3) S 42 " + strings.Repeat("0 ", 40)
	if err := os.WriteFile(filepath.Join(procPath, "stat"), []byte(stat), 0o600); err != nil {
		t.Fatalf("WriteFile(stat) error = %v", err)
	}

	ppid, err := PPID(100)
	if err != nil {
		t.Fatalf("PPID(100) error = %v", err)
	}
	if ppid != 42 {
		t.Fatalf("PPID(100) = %d, want 42", ppid)
	}
}

func TestPPIDMissing(t *testing.T) {
	tmpRoot := setProcRoot(t)
	procPath := filepath.Join(tmpRoot, "proc")
	if err := os.MkdirAll(procPath, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", procPath, err)
	}

	if _, err := PPID(100); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("PPID(100) error = %v, want fs.ErrNotExist", err)
	}
}

func setProcRoot(t *testing.T) string {
	t.Helper()
	tmpRoot := t.TempDir()
	originalPrefix := filepath.Dir(procfs.DefaultPath())
	procfs.RootPrefix(tmpRoot)
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })
	return tmpRoot
}
