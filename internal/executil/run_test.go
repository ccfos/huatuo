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
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestRunCapturesOutputAndErrors(t *testing.T) {
	tests := []struct {
		name, script, stdout, stderr string
		exitCode                     int
	}{
		{name: "success", script: `printf data; printf warning >&2`, stdout: "data", stderr: "warning"},
		{name: "nonzero exit", script: `printf data; printf warning >&2; exit 7`, stdout: "data", stderr: "warning", exitCode: 7},
		{name: "stdout tail", script: `printf 12345`, stdout: "2345"},
		{name: "stdout tail and exit", script: `printf 12345; exit 7`, stdout: "2345", exitCode: 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Run(t.Context(), Spec{Path: "/bin/sh", Args: []string{"-c", tt.script}, MaxStdoutBytes: 4})
			if result == nil || string(result.Stdout) != tt.stdout || string(result.Stderr) != tt.stderr {
				t.Fatalf("result = %+v, err = %v", result, err)
			}
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) != (tt.exitCode != 0) {
				t.Fatalf("exit error: %v", err)
			}
			if exitErr != nil && exitErr.ExitCode() != tt.exitCode {
				t.Fatalf("exit code %d", exitErr.ExitCode())
			}
			if tt.exitCode == 0 && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRunResultAvailability(t *testing.T) {
	result, err := Run(t.Context(), Spec{})
	if result != nil || err == nil {
		t.Fatalf("invalid spec = (%+v,%v)", result, err)
	}
	result, err = Run(t.Context(), Spec{Path: "/missing/executil-command"}, WithMemfdOutput(64, func(string) []string { return nil }))
	if result == nil || !errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrClosed) {
		t.Fatalf("failed start = (%+v,%v)", result, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err = Run(ctx, Spec{Path: "/bin/true"})
	var failure *RunError
	if result == nil || !errors.As(err, &failure) || !failure.IsCancellation() {
		t.Fatalf("canceled start = (%+v,%v)", result, err)
	}
}

func TestRunMemfdSnapshotsSurviveClose(t *testing.T) {
	for _, limit := range []int{4, 8} {
		result, err := Run(t.Context(), Spec{Path: "/bin/sh"}, WithMemfdOutput(limit, func(path string) []string {
			return []string{"-c", `printf profile > "$1"; printf status; printf warning >&2`, "test", path}
		}))
		if result == nil || string(result.Memfd) != "profile"[:min(limit, 7)] || string(result.Stdout) != "status" || string(result.Stderr) != "warning" {
			t.Fatalf("result = %+v, err = %v", result, err)
		}
		if errors.Is(err, ErrOutputLimitExceeded) != (limit < 7) {
			t.Fatalf("memfd limit: %v", err)
		}
	}
}

func TestRunRedirectsBorrowedWriters(t *testing.T) {
	tests := []struct {
		name                           string
		redirectStdout, redirectStderr bool
	}{
		{"nil writers preserve capture", false, false},
		{"stdout only", true, false},
		{"stderr only", false, true},
		{"both streams", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			var stdoutWriter, stderrWriter io.Writer
			wantStdout, wantStderr := "ta", "ing"
			var wantStdoutWriter, wantStderrWriter string
			if tt.redirectStdout {
				stdoutWriter = &stdout
				wantStdout, wantStdoutWriter = "", "data"
			}
			if tt.redirectStderr {
				stderrWriter = &stderr
				wantStderr, wantStderrWriter = "", "warning"
			}
			result, err := Run(t.Context(), Spec{
				Path:           "/bin/sh",
				Args:           []string{"-c", `printf data; printf warning >&2`},
				MaxStdoutBytes: 2,
				MaxStderrBytes: 3,
			}, WithStdout(stdoutWriter), WithStderr(stderrWriter))
			if err != nil {
				t.Fatal(err)
			}
			if string(result.Stdout) != wantStdout || string(result.Stderr) != wantStderr {
				t.Fatalf("captured stdout = %q, stderr = %q; want %q, %q", result.Stdout, result.Stderr, wantStdout, wantStderr)
			}
			if stdout.String() != wantStdoutWriter || stderr.String() != wantStderrWriter {
				t.Fatalf("writers = %q, %q; want %q, %q", stdout.String(), stderr.String(), wantStdoutWriter, wantStderrWriter)
			}
		})
	}
}

func TestRunDrainsOutputAndKeepsTails(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result, err := Run(ctx, Spec{
		Path:           "/bin/sh",
		Args:           []string{"-c", `head -c 2097152 /dev/zero; printf stdout; head -c 2097152 /dev/zero >&2; printf stderr >&2`},
		MaxStdoutBytes: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Stdout) != "dout" {
		t.Fatalf("stdout tail = %q, want dout", result.Stdout)
	}
	wantStderr := append(make([]byte, defaultMaxOutputBytes-len("stderr")), "stderr"...)
	if !bytes.Equal(result.Stderr, wantStderr) {
		t.Fatal("incorrect stderr tail")
	}
}

func TestRunKeepsIndependentOutputTails(t *testing.T) {
	tests := []struct {
		name                           string
		maxStdoutBytes, maxStderrBytes int
		stdout, stderr                 string
	}{
		{"defaults", 0, 0, "12345", "abcdefgh"},
		{"stdout limit", 2, 0, "45", "abcdefgh"},
		{"stderr limit", 0, 3, "12345", "fgh"},
		{"both limits", 2, 3, "45", "fgh"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Run(t.Context(), Spec{
				Path:           "/bin/sh",
				Args:           []string{"-c", `printf 12; printf abc >&2; printf 345; printf defgh >&2`},
				MaxStdoutBytes: tt.maxStdoutBytes,
				MaxStderrBytes: tt.maxStderrBytes,
			})
			if err != nil {
				t.Fatal(err)
			}
			if string(result.Stdout) != tt.stdout || string(result.Stderr) != tt.stderr {
				t.Fatalf("stdout = %q, stderr = %q; want %q, %q", result.Stdout, result.Stderr, tt.stdout, tt.stderr)
			}
		})
	}
}

type cancelOutput struct{ cancel context.CancelFunc }

func (w cancelOutput) Write(data []byte) (int, error) { w.cancel(); return len(data), nil }

func TestRunCancellationPreservesIndependentFailures(t *testing.T) {
	tests := []struct {
		name, script             string
		outputLimit, exitFailure bool
	}{
		{"pure cancellation", `trap '' TERM; printf ready >&2; while :; do :; done`, false, false},
		{"stdout tail", `trap '' TERM; printf 12345; printf ready >&2; while :; do :; done`, false, false},
		{"memfd output failure", `trap '' TERM; printf 12345 > "$1"; printf ready >&2; while :; do :; done`, true, false},
		{"execution failure", `trap 'exit 7' TERM; printf ready >&2; while :; do :; done`, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			_, err := Run(
				ctx, Spec{Path: "/bin/sh", StopGracePeriod: 50 * time.Millisecond, MaxStdoutBytes: 4},
				WithStderr(cancelOutput{cancel: cancel}),
				WithMemfdOutput(4, func(path string) []string {
					return []string{"-c", tt.script, "test", path}
				}),
			)
			var failure *RunError
			if !errors.Is(err, context.Canceled) || !errors.As(err, &failure) {
				t.Fatalf("Run(): %v", err)
			}
			if failure.IsCancellation() == (tt.outputLimit || tt.exitFailure) {
				t.Fatalf("incorrect cancellation classification: %v", err)
			}
			if errors.Is(failure.OutputErr, ErrOutputLimitExceeded) != tt.outputLimit {
				t.Fatalf("OutputErr: %v", failure.OutputErr)
			}
			if (failure.ExecutionErr != nil) != tt.exitFailure {
				t.Fatalf("ExecutionErr: %v", failure.ExecutionErr)
			}
		})
	}
}

func TestRunDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err := Run(ctx, Spec{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}, StopGracePeriod: time.Millisecond}, WithStdout(io.Discard))
	var failure *RunError
	if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &failure) || !failure.IsCancellation() {
		t.Fatalf("Run(): %v", err)
	}
}

func TestCollectResultPreservesCleanupOwnership(t *testing.T) {
	cleanupErr := errors.New("close output failed")
	executionErr := errors.New("exit failed independently")
	process, err := New(Spec{Path: "/unused/command", MaxStdoutBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	// Inject an OS cleanup failure after publishing a completed lifecycle.
	process.state = processStateFinished
	process.memfd = &memfdOutput{closeErr: cleanupErr}
	process.wait.err = executionErr
	close(process.start.done)
	close(process.wait.done)
	process.publishDone()
	if _, err := process.stdout.Write([]byte("12345")); err != nil {
		t.Fatal(err)
	}
	result, err := collectResult(process, &RunError{ContextErr: context.Canceled})
	var failure *RunError
	if result == nil || !errors.As(err, &failure) || failure.IsCancellation() || failure.Process != process {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if string(result.Stdout) != "2345" || failure.OutputErr != nil {
		t.Fatalf("stdout = %q, output error = %v", result.Stdout, failure.OutputErr)
	}
	for _, cause := range []error{context.Canceled, executionErr, cleanupErr} {
		if !errors.Is(err, cause) {
			t.Errorf("lost %v: %v", cause, err)
		}
	}
	if err := failure.Process.Close(); !errors.Is(err, cleanupErr) {
		t.Fatalf("retry Close(): %v", err)
	}
}

func TestRunErrorClassification(t *testing.T) {
	failureErr := errors.New("independent failure")
	tests := []struct {
		name    string
		failure RunError
		want    bool
	}{
		{"cancellation", RunError{ContextErr: context.Canceled}, true},
		{"deadline", RunError{ContextErr: context.DeadlineExceeded}, true},
		{"execution", RunError{ContextErr: context.Canceled, ExecutionErr: failureErr}, false},
		{"output", RunError{ContextErr: context.Canceled, OutputErr: failureErr}, false},
		{"cleanup", RunError{ContextErr: context.Canceled, CleanupErr: failureErr}, false},
		{"no error", RunError{}, false},
	}
	for i := range tests {
		tt := &tests[i]
		t.Run(tt.name, func(t *testing.T) {
			if tt.failure.IsCancellation() != tt.want {
				t.Fatal("incorrect cancellation classification")
			}
			if !tt.want && tt.name != "no error" && !errors.Is(&tt.failure, failureErr) {
				t.Fatal("lost independent failure")
			}
		})
	}
}

func TestCanceledLaunchPreservesLaterExitFailure(t *testing.T) {
	process, err := New(Spec{Path: "/unused/command"})
	if err != nil {
		t.Fatal(err)
	}
	stopErr := wrapStopFailure(errors.New("signal failed during launch"))
	exitErr := errors.New("command exited with a separate failure")
	process.state = processStateFinished
	process.start.err = &RunError{ContextErr: context.Canceled, CleanupErr: stopErr}
	process.wait.err = exitErr
	close(process.start.done)
	close(process.wait.done)
	process.publishDone()
	if err := process.Wait(); !errors.Is(err, exitErr) || !errors.Is(err, context.Canceled) || !errors.Is(err, stopErr) {
		t.Fatalf("Wait(): %v", err)
	}
	_, err = collectResult(process, process.start.err)
	var failure *RunError
	if !errors.As(err, &failure) || !errors.Is(failure.ExecutionErr, exitErr) || !errors.Is(failure.CleanupErr, stopErr) || failure.Process != nil {
		t.Fatalf("recovered result: %v", err)
	}
}
