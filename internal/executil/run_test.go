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
	"fmt"
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
	for _, tt := range []struct {
		name    string
		spec    Spec
		options []Option
	}{
		{name: "invalid spec"},
		{name: "invalid memfd option", spec: Spec{Path: "/bin/true"}, options: []Option{WithMemfdOutput(64, nil)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Run(t.Context(), tt.spec, tt.options...)
			if result == nil || err == nil {
				t.Fatalf("Run() = (%+v,%v), want non-nil result and configuration error", result, err)
			}
			if len(result.Stdout) != 0 || len(result.Stderr) != 0 || len(result.Memfd) != 0 {
				t.Fatalf("Run() result = %+v, want empty output for invalid configuration", result)
			}
		})
	}
	result, err := Run(t.Context(), Spec{Path: "/missing/executil-command"}, WithMemfdOutput(64, func(string) []string { return nil }))
	if result == nil || !errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrClosed) {
		t.Fatalf("failed start = (%+v,%v)", result, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err = Run(ctx, Spec{Path: "/bin/true"})
	if result == nil || !errors.Is(err, context.Canceled) ||
		!errors.Is(err, ErrRunCanceled) || errors.Is(err, ErrRunTimeout) {
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
				func(process *Process) {
					// Output can precede Start's launch result; cancel only after launch.
					process.stderr = cancelOutput{cancel: func() {
						<-process.startDone
						cancel()
					}}
				},
				WithMemfdOutput(4, func(path string) []string {
					return []string{"-c", tt.script, "test", path}
				}),
			)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Run(): %v", err)
			}
			if errors.Is(err, ErrRunCanceled) == (tt.outputLimit || tt.exitFailure) || errors.Is(err, ErrRunTimeout) {
				t.Fatalf("incorrect cancellation classification: %v", err)
			}
			if errors.Is(err, ErrOutputLimitExceeded) != tt.outputLimit {
				t.Fatalf("output error: %v", err)
			}
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) != tt.exitFailure {
				t.Fatalf("exit error: %v", err)
			}
			if exitErr != nil && exitErr.ExitCode() != 7 {
				t.Fatalf("exit code = %d, want 7", exitErr.ExitCode())
			}
		})
	}
}

func TestRunDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err := Run(ctx, Spec{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}, StopGracePeriod: time.Millisecond}, WithStdout(io.Discard))
	if !errors.Is(err, context.DeadlineExceeded) ||
		!errors.Is(err, ErrRunTimeout) || errors.Is(err, ErrRunCanceled) {
		t.Fatalf("Run(): %v", err)
	}
}

func TestFinishRunRejectsContextStopWithCleanupFailure(t *testing.T) {
	cleanupErr := errors.New("close output failed")
	for _, contextErr := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(contextErr.Error(), func(t *testing.T) {
			process, err := New(Spec{Path: "/unused/command"})
			if err != nil {
				t.Fatal(err)
			}
			process.state = processRunStateFinished
			process.memfd = &memfdOutput{closeErr: cleanupErr}
			close(process.startDone)
			close(process.waitDone)
			process.publishDone()

			_, err = finishRun(process, contextErr, nil)
			if !errors.Is(err, ErrCleanupFailed) ||
				!errors.Is(err, contextErr) || !errors.Is(err, cleanupErr) {
				t.Fatalf("finishRun() error = %v, want context and cleanup failure", err)
			}
			if errors.Is(err, ErrRunCanceled) || errors.Is(err, ErrRunTimeout) {
				t.Fatalf("finishRun() incorrectly marked a cleanup failure as a normal context stop: %v", err)
			}
		})
	}
}

func TestFinishRunPreservesCleanupCauses(t *testing.T) {
	cleanupErr := errors.New("close output failed")
	executionErr := errors.New("exit failed independently")
	process, err := New(Spec{Path: "/unused/command", MaxStdoutBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	// Inject an OS cleanup failure after publishing a completed lifecycle.
	process.state = processRunStateFinished
	process.memfd = &memfdOutput{closeErr: cleanupErr}
	process.terminalErr = executionErr
	close(process.startDone)
	close(process.waitDone)
	process.publishDone()
	if _, err := process.stdout.Write([]byte("12345")); err != nil {
		t.Fatal(err)
	}
	result, err := finishRun(process, context.Canceled, nil)
	if result == nil || !errors.Is(err, ErrCleanupFailed) ||
		errors.Is(err, ErrRunCanceled) || errors.Is(err, ErrRunTimeout) {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if string(result.Stdout) != "2345" {
		t.Fatalf("stdout = %q, want 2345", result.Stdout)
	}
	for _, cause := range []error{context.Canceled, executionErr, cleanupErr} {
		if !errors.Is(err, cause) {
			t.Errorf("lost %v: %v", cause, err)
		}
	}

	if err := process.Close(); !errors.Is(err, cleanupErr) {
		t.Fatalf("retry Close(): %v", err)
	}
}

func TestCanceledLaunchPreservesLaterExitFailure(t *testing.T) {
	process, err := New(Spec{Path: "/unused/command"})
	if err != nil {
		t.Fatal(err)
	}
	stopErr := errors.Join(ErrStopFailed, errors.New("signal failed during launch"))
	exitErr := errors.New("command exited with a separate failure")
	launchErr := errors.Join(context.Canceled, stopErr)
	process.startCancelErr = context.Canceled
	process.terminalErr = launchErr
	process.terminalErr = errors.Join(process.terminalErr, exitErr)
	process.state = processRunStateFinished
	close(process.startDone)
	close(process.waitDone)
	process.publishDone()
	if err := process.Wait(); !errors.Is(err, exitErr) || !errors.Is(err, context.Canceled) || !errors.Is(err, stopErr) {
		t.Fatalf("Wait(): %v", err)
	}
	_, err = finishRun(process, nil, nil)
	if !errors.Is(err, exitErr) || !errors.Is(err, stopErr) || errors.Is(err, ErrCleanupFailed) {
		t.Fatalf("recovered result: %v", err)
	}
	if errors.Is(err, ErrRunCanceled) || errors.Is(err, ErrRunTimeout) {
		t.Fatalf("recovered result incorrectly marked as a normal context stop: %v", err)
	}
	if errors.Is(launchErr, exitErr) {
		t.Fatal("later completion mutated the published launch error")
	}
}

type failingOutput struct {
	err error
}

func (w failingOutput) Write([]byte) (int, error) { return 0, w.err }

func TestRunDoesNotClassifyWriterFailureAsLaunchCancellation(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, fmt.Errorf("%w: %w", ErrCleanupFailed, context.Canceled)} {
		t.Run(cause.Error(), func(t *testing.T) {
			for _, tt := range []struct {
				name string
				err  error
			}{
				{"direct", cause},
				{"wrapped", fmt.Errorf("writer rejected output: %w", cause)},
				{"joined", errors.Join(cause, errors.New("writer failed"))},
				{"multiple wrapped causes", fmt.Errorf("writer: %w: %w", cause, errors.New("writer failed"))},
			} {
				t.Run(tt.name, func(t *testing.T) {
					_, err := Run(t.Context(), Spec{Path: "/bin/sh", Args: []string{"-c", "printf output"}}, WithStdout(failingOutput{err: tt.err}))
					if !errors.Is(err, tt.err) || !errors.Is(err, cause) {
						t.Fatalf("Run lost the independent writer failure: %v", err)
					}
					if errors.Is(err, ErrRunCanceled) || errors.Is(err, ErrRunTimeout) {
						t.Fatalf("Run classified independent error metadata as launch cancellation: %v", err)
					}
				})
			}
		})
	}
}

func TestFinishRunPreservesLaunchCancellationFailures(t *testing.T) {
	independentErr := errors.New("command failed independently")
	cleanupErr := fmt.Errorf("%w: %w", ErrCleanupFailed, independentErr)
	for _, marker := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(marker.Error(), func(t *testing.T) {
			for _, failure := range []struct {
				name string
				err  error
			}{
				{"execution", independentErr},
				{"cleanup", cleanupErr},
			} {
				t.Run(failure.name, func(t *testing.T) {
					process, err := New(Spec{Path: "/unused/command"})
					if err != nil {
						t.Fatal(err)
					}
					process.startCancelErr = marker
					process.terminalErr = errors.Join(marker, failure.err)
					process.state = processRunStateFinished
					close(process.startDone)
					close(process.waitDone)
					process.publishDone()

					_, err = finishRun(process, nil, nil)
					if !errors.Is(err, marker) || !errors.Is(err, failure.err) {
						t.Fatalf("finishRun lost cancellation or failure: %v", err)
					}
					if errors.Is(err, ErrRunCanceled) || errors.Is(err, ErrRunTimeout) {
						t.Fatalf("finishRun suppressed an independent failure: %v", err)
					}
					if errors.Is(err, ErrCleanupFailed) != errors.Is(failure.err, ErrCleanupFailed) {
						t.Fatalf("finishRun changed cleanup classification: %v", err)
					}
				})
			}
		})
	}
}

func TestFinishRunClassifiesCancellationAfterCleanup(t *testing.T) {
	pathErr := &os.PathError{Op: "close", Path: "memfd", Err: os.ErrClosed}
	for _, contextErr := range []error{nil, context.Canceled, context.DeadlineExceeded} {
		for _, failureErr := range []error{
			nil,
			pathErr,
			ErrOutputLimitExceeded,
			fmt.Errorf("%w: %w", ErrCleanupFailed, pathErr),
			context.Canceled,
			fmt.Errorf("writer failed: %w", ErrStopped),
			errors.Join(ErrStopped, pathErr),
			fmt.Errorf("%w: %w", ErrCleanupFailed, ErrStopped),
		} {
			name := fmt.Sprintf("context=%v/failure=%v", contextErr, failureErr)
			t.Run(name, func(t *testing.T) {
				process, err := New(Spec{Path: "/unused/command"})
				if err != nil {
					t.Fatal(err)
				}
				process.state = processRunStateFinished
				process.terminalErr = failureErr
				close(process.startDone)
				close(process.waitDone)
				process.publishDone()
				_, err = finishRun(process, contextErr, nil)
				if errors.Is(err, ErrRunCanceled) != (contextErr != nil && errors.Is(contextErr, context.Canceled) && failureErr == nil) ||
					errors.Is(err, ErrRunTimeout) != (contextErr != nil && errors.Is(contextErr, context.DeadlineExceeded) && failureErr == nil) {
					t.Fatalf("incorrect final cancellation marker: %v", err)
				}
				for _, cause := range []error{contextErr, failureErr} {
					if cause != nil && !errors.Is(err, cause) {
						t.Errorf("lost cause %v: %v", cause, err)
					}
				}
				if contextErr == nil && failureErr == nil && err != nil {
					t.Fatalf("finishRun() = %v, want nil", err)
				}
			})
		}
	}
}
