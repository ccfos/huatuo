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
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/ccfos/huatuo/internal/procfs"
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

func TestCommandLine(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		want    string
		wantErr bool
	}{
		{
			name: "multiple arguments",
			data: []byte("/usr/bin/docker\x00run\x00--rm\x00alpine\x00"),
			want: "/usr/bin/docker run --rm alpine ",
		},
		{
			name: "empty command line",
		},
		{
			name: "truncate and sanitize",
			data: func() []byte {
				data := bytes.Repeat([]byte{'a'}, 130)
				data[5] = 0
				data[127] = 0
				return data
			}(),
			want: strings.Repeat("a", 5) + " " + strings.Repeat("a", 121) + " ",
		},
		{
			name:    "missing process",
			wantErr: true,
		},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tmpRoot := setProcRoot(t)
			pid := 100 + index
			if !test.wantErr {
				cmdlinePath := filepath.Join(tmpRoot, "proc", strconv.Itoa(pid), "cmdline")
				if err := os.MkdirAll(filepath.Dir(cmdlinePath), 0o755); err != nil {
					t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(cmdlinePath), err)
				}
				if err := os.WriteFile(cmdlinePath, test.data, 0o600); err != nil {
					t.Fatalf("WriteFile(%q) error = %v", cmdlinePath, err)
				}
			}

			got, err := CommandLine(pid)
			if test.wantErr {
				if err == nil {
					t.Fatalf("CommandLine(%d) error = nil, want non-nil", pid)
				}
				return
			}
			if err != nil {
				t.Fatalf("CommandLine(%d) error = %v", pid, err)
			}
			if got != test.want {
				t.Fatalf("CommandLine(%d) = %q, want %q", pid, got, test.want)
			}
		})
	}
}

func TestHostname(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("UTS namespace lookup requires Linux")
	}

	pid := os.Getpid()
	if _, err := Hostname(pid); errors.Is(err, syscall.EPERM) {
		t.Skip("setns requires CAP_SYS_ADMIN")
	}

	want, err := os.Hostname()
	if err != nil {
		t.Fatalf("Hostname() error = %v", err)
	}
	got, err := Hostname(pid)
	if err != nil {
		t.Fatalf("Hostname(%d) error = %v", pid, err)
	}
	if got != want {
		t.Fatalf("Hostname(%d) = %q, want %q", pid, got, want)
	}

	if got, err := Hostname(99999999); err == nil || got != "" {
		t.Fatalf("Hostname(missing) = %q, %v, want empty result and error", got, err)
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
