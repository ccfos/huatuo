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

package exec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFormatCmdIncludesExecutableAndArguments(t *testing.T) {
	t.Parallel()

	got := formatCmd(
		"/opt/async-profiler/bin/asprof",
		[]string{"dump", "-f", "/tmp/profile.collapsed", "164879"},
	)
	want := "/opt/async-profiler/bin/asprof dump -f /tmp/profile.collapsed 164879"
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
			output: []byte("\n Profiling started \t\n"),
			want:   "Profiling started",
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

func TestExecCmdsUsesStopProfilerResultAfterCancellation(t *testing.T) {
	tests := []struct {
		name        string
		stopExit    int
		wantSuccess bool
		wantErr     string
	}{
		{name: "stop succeeds", wantSuccess: true},
		{name: "stop fails", stopExit: 23, wantErr: "exit status 23"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asprofPath := filepath.Join(t.TempDir(), "asprof")
			script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--libpath" ]; then
	exit %d
fi
trap 'exit 0' TERM
while :; do sleep 1; done
`, tt.stopExit)
			if err := os.WriteFile(asprofPath, []byte(script), 0o600); err != nil {
				t.Fatalf("write fake asprof: %v", err)
			}
			if err := os.Chmod(asprofPath, 0o700); err != nil {
				t.Fatalf("make fake asprof executable: %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()

			results := ExecCmds(ctx, []int{164879}, asprofPath, func(int) []string {
				return []string{"start"}
			})
			if len(results) != 1 {
				t.Fatalf("ExecCmds() returned %d results, want 1", len(results))
			}

			result := results[0]
			if result.Success != tt.wantSuccess {
				t.Errorf("ExecCmds() Success=%t, want %t", result.Success, tt.wantSuccess)
			}
			if tt.wantErr == "" {
				if result.CmdErr != nil {
					t.Errorf("ExecCmds() CmdErr=%v, want nil", result.CmdErr)
				}
				return
			}
			if result.CmdErr == nil || !strings.Contains(result.CmdErr.Error(), tt.wantErr) {
				t.Errorf("ExecCmds() CmdErr=%v, want substring %q", result.CmdErr, tt.wantErr)
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
			Cmd:     "/opt/async-profiler/bin/asprof start 164879",
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
		`command "/opt/async-profiler/bin/asprof start 164879" failed for pid 164879`,
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
		{Pid: 101, Cmd: "asprof start 101", CmdErr: firstErr},
		{Pid: 202, Cmd: "asprof start 202", CmdErr: secondErr},
		{Pid: 303, Cmd: "asprof start 303", Success: true},
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
