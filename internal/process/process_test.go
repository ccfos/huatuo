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
	"bufio"
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

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
			name: "single null argument",
			data: []byte{0},
			want: " ",
		},
		{
			name: "exact prefix length",
			data: bytes.Repeat([]byte{'x'}, 128),
			want: strings.Repeat("x", 128),
		},
		{
			name: "large command line",
			data: bytes.Repeat([]byte("arg\x00"), 256*1024),
			want: strings.Repeat("arg ", 32),
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

func setProcRoot(t testing.TB) string {
	t.Helper()
	tmpRoot := t.TempDir()
	originalPrefix := filepath.Dir(procfs.DefaultPath())
	procfs.RootPrefix(tmpRoot)
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })
	return tmpRoot
}

func TestCommandLineStopsAtPrefix(t *testing.T) {
	root := setProcRoot(t)
	path := filepath.Join(root, "proc", "100", "cmdline")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}

	var result string
	var readErr error
	done := make(chan struct{})
	go func() {
		result, readErr = CommandLine(100)
		close(done)
	}()

	writer, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = writer.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("command-line reader did not stop after writer cleanup")
		}
	})

	prefix := bytes.Repeat([]byte("arg\x00"), 32)
	if _, err := writer.Write(prefix); err != nil {
		t.Fatal(err)
	}

	// Keep the writer open: returning must depend on the prefix, not EOF.
	select {
	case <-done:
		if readErr != nil || result != strings.Repeat("arg ", 32) {
			t.Fatalf("CommandLine() = %q, %v", result, readErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("command-line reader waited beyond its 128-byte prefix")
	}
}

func TestCommandLineReadError(t *testing.T) {
	root := setProcRoot(t)
	if err := os.MkdirAll(filepath.Join(root, "proc", "100", "cmdline"), 0o755); err != nil {
		t.Fatal(err)
	}

	if result, err := CommandLine(100); result != "" || !errors.Is(err, syscall.EISDIR) {
		t.Fatalf("CommandLine(directory) = %q, %v, want EISDIR", result, err)
	}
}

func TestCommandLineRealProcess(t *testing.T) {
	if os.Getenv("HUATUO_COMMANDLINE_CHILD") == "1" {
		_, _ = os.Stdout.WriteString("ready\n")
		time.Sleep(30 * time.Second)
		return
	}

	originalPrefix := filepath.Dir(procfs.DefaultPath())
	procfs.RootPrefix("/")
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })

	child := exec.CommandContext(
		t.Context(),
		os.Args[0],
		"-test.run=^TestCommandLineRealProcess$",
		"--",
		strings.Repeat("x", 64*1024),
	)
	child.Env = append(os.Environ(), "HUATUO_COMMANDLINE_CHILD=1")
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}

	if err := child.Start(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})

	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("child ready = %q, %v", line, err)
	}

	pid := child.Process.Pid
	raw, err := os.ReadFile(procfs.Path(strconv.Itoa(pid), "cmdline"))
	if err != nil {
		t.Fatal(err)
	}

	if len(raw) <= 64*1024 {
		t.Fatalf("child command line has only %d bytes", len(raw))
	}

	want := string(bytes.ReplaceAll(raw[:128], []byte{0}, []byte{' '}))
	if result, err := CommandLine(pid); err != nil || result != want {
		t.Fatalf("CommandLine(real child) = %q, %v, want %q", result, err, want)
	}
}

func BenchmarkCommandLinePrefix(b *testing.B) {
	for _, size := range []struct {
		name  string
		bytes int
	}{
		{
			name:  "short",
			bytes: 128,
		},
		{
			name:  "large",
			bytes: 1024 * 1024,
		},
	} {
		b.Run(size.name, func(b *testing.B) {
			root := setProcRoot(b)
			path := filepath.Join(root, "proc", "100", "cmdline")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				b.Fatal(err)
			}

			if err := os.WriteFile(path, bytes.Repeat([]byte{'x'}, size.bytes), 0o600); err != nil {
				b.Fatal(err)
			}

			b.ReportAllocs()
			for b.Loop() {
				if result, err := CommandLine(100); err != nil || len(result) != 128 {
					b.Fatalf("CommandLine() returned %d bytes, %v", len(result), err)
				}
			}
		})
	}
}
