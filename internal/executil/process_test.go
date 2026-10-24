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
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestProcessRejectsWaitAndStopBeforeStart(t *testing.T) {
	process, err := New(Spec{Path: "/missing/huatuo-command"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if err := process.Wait(); !errors.Is(err, ErrProcessNotStarted) {
		t.Errorf("Wait() error = %v, want not-started error", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := process.Stop(ctx); !errors.Is(err, ErrProcessNotStarted) {
		t.Errorf("Stop() error = %v, want not-started error", err)
	}
}

func TestProcessZeroValueReturnsInitializationError(t *testing.T) {
	tests := []struct {
		name string
		call func(*Process) error
	}{
		{
			name: "start",
			call: func(process *Process) error {
				return process.Start(context.Background())
			},
		},
		{
			name: "wait",
			call: func(process *Process) error {
				return process.Wait()
			},
		},
		{
			name: "stop",
			call: func(process *Process) error {
				return process.Stop(context.Background())
			},
		},
		{
			name: "close",
			call: func(process *Process) error {
				return process.Close()
			},
		},
		{
			name: "memfd output",
			call: func(process *Process) error {
				_, err := process.MemfdOutput()
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var process Process
			if err := test.call(&process); !errors.Is(err, ErrProcessNotInitialized) {
				t.Errorf("operation error = %v, want ErrProcessNotInitialized", err)
			}
		})
	}

	var process Process
	if got := process.Stdout(); len(got) != 0 {
		t.Errorf("Stdout() = %q, want empty output", got)
	}

	if got := process.Stderr(); len(got) != 0 {
		t.Errorf("Stderr() = %q, want empty output", got)
	}
}

func TestProcessStartRejectsCanceledContextAndRetry(t *testing.T) {
	for _, tt := range []struct {
		name       string
		contextErr error
	}{
		{"cancellation", context.Canceled},
		{"deadline", context.DeadlineExceeded},
	} {
		t.Run(tt.name, func(t *testing.T) {
			process, err := New(Spec{Path: "/bin/true"})
			if err != nil {
				t.Fatal(err)
			}
			var ctx context.Context
			var cancel context.CancelFunc
			if errors.Is(tt.contextErr, context.Canceled) {
				ctx, cancel = context.WithCancel(t.Context())
				cancel()
			} else {
				ctx, cancel = context.WithDeadline(t.Context(), time.Time{})
			}
			defer cancel()
			for _, operation := range []func() error{
				func() error { return process.Start(ctx) },
				process.Wait,
			} {
				if err := operation(); err != tt.contextErr { //nolint:errorlint // Verify direct, shared cancellation markers.
					t.Fatalf("lifecycle error = %v, want %v", err, tt.contextErr)
				}
			}
			for _, err := range []error{process.Stop(t.Context()), process.Close()} {
				if err != nil {
					t.Fatalf("cleanup repeated launch cancellation: %v", err)
				}
			}
			if err := process.Start(t.Context()); !errors.Is(err, ErrProcessAlreadyStarted) {
				t.Fatalf("second Start() = %v, want already-started error", err)
			}
		})
	}
}

func TestProcessWaitOwnsReaperCleanupFailure(t *testing.T) {
	cleanupErr := errors.New("group cleanup failed")
	for _, startErr := range []error{nil, context.Canceled} {
		name := "natural exit"
		if startErr != nil {
			name = "canceled start"
		}

		t.Run(name, func(t *testing.T) {
			process, err := New(Spec{Path: "/unused/command"})
			if err != nil {
				t.Fatal(err)
			}
			// Publish a completed lifecycle without launching an OS process.
			process.state = processRunStateFinished
			process.terminalErr = errors.Join(startErr, fmt.Errorf("%w: %w", ErrCleanupFailed, errors.Join(ErrStopFailed, cleanupErr)))
			close(process.startDone)
			close(process.waitDone)
			process.publishDone()
			for range 2 {
				err = process.Wait()
				if !errors.Is(err, ErrStopFailed) || !errors.Is(err, cleanupErr) {
					t.Fatalf("Wait() error = %v, want stored cleanup failure", err)
				}

				if startErr != nil && !errors.Is(err, startErr) {
					t.Errorf("Wait() error = %v, want start failure", err)
				}

				if !errors.Is(err, ErrCleanupFailed) {
					t.Fatalf("Wait lost cleanup classification: %v", err)
				}
				for _, err := range []error{process.Stop(t.Context()), process.Close()} {
					if err != nil {
						t.Errorf("cleanup repeated a reaper failure: %v", err)
					}
				}
			}
		})
	}
}

func TestProcessStdoutKeepsOutputTail(t *testing.T) {
	for _, output := range []string{"123", "1234", "12345"} {
		t.Run(output, func(t *testing.T) {
			process, err := New(Spec{Path: "/unused/command", MaxStdoutBytes: 4})
			if err != nil {
				t.Fatal(err)
			}

			if _, err := process.stdout.Write([]byte(output)); err != nil {
				t.Fatal(err)
			}

			// Publish captured output without depending on an OS subprocess.
			process.state = processRunStateFinished
			close(process.startDone)
			close(process.waitDone)
			process.publishDone()
			if err := process.Wait(); err != nil {
				t.Fatalf("Wait() reported an output size error: %v", err)
			}

			got := process.Stdout()
			if string(got) != output[max(0, len(output)-4):] {
				t.Errorf("Stdout() = %q, want retained output tail", got)
			}
		})
	}
}

func TestMayAppendCleanupErrorPreservesExecutionResult(t *testing.T) {
	executionErr := errors.New("command failed")
	cleanupErr := errors.New("cleanup failed")
	markedErr := fmt.Errorf("%w: %w", ErrCleanupFailed, cleanupErr)
	for _, tt := range []struct {
		name string
		err  error
	}{
		{"nil", nil},
		{"unmarked", cleanupErr},
		{"already marked", markedErr},
	} {
		t.Run(tt.name, func(t *testing.T) {
			process := &Process{terminalErr: executionErr}
			process.mu.Lock()
			mayAppendCleanupError(process, tt.err)
			process.mu.Unlock()

			if !errors.Is(process.terminalErr, executionErr) {
				t.Fatalf("lost execution failure: %v", process.terminalErr)
			}
			got := process.terminalErr
			if tt.err == nil {
				if errors.Is(got, ErrCleanupFailed) {
					t.Fatalf("nil cleanup error changed classification: %v", got)
				}
				return
			}
			if !errors.Is(got, ErrCleanupFailed) || !errors.Is(got, tt.err) {
				t.Fatalf("incorrect cleanup classification: %v", got)
			}
		})
	}
}

type pidGateContext struct {
	context.Context

	reached chan struct{}
	release chan struct{}
	once    sync.Once

	pid *int
}

func (g *pidGateContext) Err() error {
	if g.pid == nil {
		return g.Context.Err()
	}

	if pid := *g.pid; pid > 0 {
		g.once.Do(func() { close(g.reached) })
		select {
		case <-g.release:
		case <-g.Context.Done():
			return g.Context.Err()
		}
		return context.Canceled
	}

	return g.Context.Err()
}

// TestProcessStartCanceledAfterLaunch verifies cancellation that wins the
// race after cmd.Start but before the process is published as running is
// reported by Start and reclaims the child process.
func TestProcessStartCanceledAfterLaunch(t *testing.T) {
	process, err := New(Spec{Path: "/bin/sleep", Args: []string{"30"}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	gate := &pidGateContext{
		Context: t.Context(),
		reached: make(chan struct{}),
		release: make(chan struct{}),
		pid:     &process.pid,
	}
	cleanup := sync.OnceFunc(func() { close(gate.release) })
	defer cleanup()

	startErr := make(chan error, 1)
	go func() {
		defer cleanup()
		startErr <- process.Start(gate)
	}()

	for i := 0; i < 200; i++ {
		process.mu.Lock()
		pid := process.pid
		process.mu.Unlock()
		if pid > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	select {
	case <-gate.reached:
	case <-time.After(5 * time.Second):
		cleanup()
		t.Fatal("Start() did not reach the post-Start launch window")
	}

	select {
	case <-process.Done():
		cleanup()
		t.Fatal("process became terminal while the launch window was open")
	default:
	}

	cleanup()

	select {
	case err := <-startErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Start() error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start() did not return after cancellation")
	}

	select {
	case <-process.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("process was not reaped after a cancelled launch")
	}
	if err := process.Wait(); err == nil {
		t.Fatal("Wait() = nil, want the launch cancellation error")
	}
}
