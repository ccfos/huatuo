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

package executil

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"huatuo-bamai/internal/procfs"

	"github.com/stretchr/testify/assert"
)

func withProcRoot(t *testing.T, root string) {
	originalPrefix := filepath.Dir(procfs.DefaultPath())
	procfs.RootPrefix(root)
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })
}

func writeCmdline(t *testing.T, procRoot string, pid uint32, data []byte) {
	path := filepath.Join(procRoot, fmt.Sprintf("%d", pid), "cmdline")
	assert.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	assert.NoError(t, os.WriteFile(path, data, 0o600))
}

func assertNoErrorNotEmpty(t *testing.T, got string, err error) {
	assert.NoError(t, err)
	assert.NotEmpty(t, got)
}

func assertErrorEmpty(t *testing.T, got string, err error) {
	assert.Error(t, err)
	assert.Empty(t, got)
}

func TestFormatCmdIncludesExecutableAndArguments(t *testing.T) {
	t.Parallel()

	got := formatCmd("/usr/bin/tool", []string{"trace", "--duration", "10"})
	want := "/usr/bin/tool trace --duration 10"
	if got != want {
		t.Fatalf("formatCmd()=%q, want %q", got, want)
	}
}

func TestCommandOutputForError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		output []byte
		want   string
	}{
		{
			name:   "trims surrounding whitespace",
			output: []byte("\n command output \t\n"),
			want:   "command output",
		},
		{
			name:   "truncates oversized output",
			output: []byte(strings.Repeat("x", maxCommandOutputInError+1)),
			want:   strings.Repeat("x", maxCommandOutputInError) + "... (truncated)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := commandOutputForError(tt.output); got != tt.want {
				t.Fatalf("commandOutputForError() length=%d, want length=%d", len(got), len(tt.want))
			}
		})
	}
}

func TestVerifyResultsIncludesFailureDetails(t *testing.T) {
	t.Parallel()

	cmdErr := errors.New("exit status 1")
	err := VerifyResults([]CmdResult{
		{
			Pid:     164879,
			Cmd:     "/usr/bin/tool trace 164879",
			Stdout:  []byte("diagnostic output\n"),
			Stderr:  []byte("attach failed\n"),
			Success: false,
			CmdErr:  cmdErr,
		},
	})
	if err == nil {
		t.Fatal("VerifyResults() error=nil, want non-nil")
	}
	if !errors.Is(err, cmdErr) {
		t.Fatalf("VerifyResults() error=%v, want wrapped command error", err)
	}

	for _, want := range []string{
		`command "/usr/bin/tool trace 164879" failed for pid 164879`,
		"exit status 1",
		`stderr="attach failed"`,
		`stdout="diagnostic output"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("VerifyResults() error=%q, want substring %q", err, want)
		}
	}
}

func TestVerifyResultsIncludesEveryFailure(t *testing.T) {
	t.Parallel()

	firstErr := errors.New("first failure")
	secondErr := errors.New("second failure")
	err := VerifyResults([]CmdResult{
		{Pid: 101, Cmd: "tool start 101", CmdErr: firstErr},
		{Pid: 202, Cmd: "tool start 202", CmdErr: secondErr},
		{Pid: 303, Cmd: "tool start 303", Success: true},
	})
	if err == nil {
		t.Fatal("VerifyResults() error=nil, want non-nil")
	}
	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("VerifyResults() error=%v, want both command errors", err)
	}
	for _, want := range []string{"pid 101", "pid 202"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("VerifyResults() error=%q, want substring %q", err, want)
		}
	}
}

func TestRunningDir(t *testing.T) {
	dir, err := RunningDir()
	assertNoErrorNotEmpty(t, dir, err)
	assert.True(t, filepath.IsAbs(dir))

	info, err := os.Stat(dir)
	assert.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestProcNameByPid_Filesystem(t *testing.T) {
	procRoot := filepath.Join(t.TempDir(), "proc")
	withProcRoot(t, filepath.Dir(procRoot))

	tests := []struct {
		name     string
		pid      uint32
		setup    func(*testing.T, uint32)
		validate func(*testing.T, string, error)
	}{
		{
			name: "ok/multi-argument cmdline",
			pid:  1002,
			setup: func(t *testing.T, pid uint32) {
				writeCmdline(t, procRoot, pid, []byte("/usr/bin/docker\x00run\x00--rm\x00alpine\x00"))
			},
			validate: func(t *testing.T, got string, err error) {
				assert.NoError(t, err)
				assert.Equal(t, "/usr/bin/docker run --rm alpine ", got)
			},
		},
		{
			name: "ok/empty cmdline",
			pid:  1003,
			setup: func(t *testing.T, pid uint32) {
				writeCmdline(t, procRoot, pid, nil)
			},
			validate: func(t *testing.T, got string, err error) {
				assert.NoError(t, err)
				assert.Equal(t, "", got)
			},
		},
		{
			name: "ok/truncate and sanitize",
			pid:  1004,
			setup: func(t *testing.T, pid uint32) {
				longCmdline := bytes.Repeat([]byte{'a'}, 130)
				longCmdline[5] = 0
				longCmdline[127] = 0
				writeCmdline(t, procRoot, pid, longCmdline)
			},
			validate: func(t *testing.T, got string, err error) {
				assert.NoError(t, err)
				assert.Len(t, got, 128)
				assert.NotContains(t, got, string(rune(0)))
				assert.Equal(t, byte(' '), got[5])
				assert.Equal(t, byte(' '), got[127])
			},
		},
		{
			name:     "invalid pid",
			pid:      1999,
			setup:    func(_ *testing.T, pid uint32) {},
			validate: assertErrorEmpty,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup(t, tt.pid)
			got, err := ProcNameByPid(tt.pid)
			tt.validate(t, got, err)
		})
	}
}

func TestHostnameByPid(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("only meaningful on Linux / requires CAP_SYS_ADMIN or proper namespace access")
	}

	selfPid := uint32(os.Getpid())

	// setns(2) requires CAP_SYS_ADMIN; skip the whole test in unprivileged environments.
	if _, err := HostnameByPid(selfPid); errors.Is(err, syscall.EPERM) {
		t.Skip("setns requires CAP_SYS_ADMIN; skipping in unprivileged environment")
	}

	tests := []struct {
		name     string
		pid      uint32
		validate func(*testing.T, string, string, error)
	}{
		{
			name: "self pid - should get current hostname",
			pid:  selfPid,
			validate: func(t *testing.T, got, expected string, err error) {
				assertNoErrorNotEmpty(t, got, err)
				assert.Equal(t, got, expected)
			},
		},
		{
			name: "invalid pid",
			pid:  99999999,
			validate: func(t *testing.T, got, expected string, err error) {
				assertErrorEmpty(t, got, err)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			currentHost, err := os.Hostname()
			assert.NoError(t, err)
			got, err := HostnameByPid(tt.pid)
			tt.validate(t, got, currentHost, err)
		})
	}
}
