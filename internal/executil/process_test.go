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
	"strings"
	"testing"
	"time"
)

func TestProcessRejectsWaitAndStopBeforeStart(t *testing.T) {
	process, err := New(Spec{Path: "/missing/huatuo-command"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if err := process.Wait(); err == nil || !strings.Contains(err.Error(), "has not been started") {
		t.Errorf("Wait() error = %v, want not-started error", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := process.Stop(ctx); err == nil || !strings.Contains(err.Error(), "has not been started") {
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
			name: "run",
			call: func(process *Process) error {
				return process.Run(context.Background())
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var process Process
			if err := test.call(&process); !errors.Is(err, errProcessNotInitialized) {
				t.Errorf("operation error = %v, want errProcessNotInitialized", err)
			}
		})
	}

	var process Process
	if got, err := process.Stdout(); len(got) != 0 || !errors.Is(err, errProcessNotInitialized) {
		t.Errorf("Stdout() = (%q, %v), want empty output and initialization error", got, err)
	}

	if got := process.Stderr(); len(got) != 0 {
		t.Errorf("Stderr() = %q, want empty output", got)
	}
}

func TestProcessStartRejectsCanceledContextAndRetry(t *testing.T) {
	process, err := New(Spec{Path: "/bin/true"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err = process.Start(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() error = %v, want context.Canceled", err)
	}

	if err := process.Start(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "already been started") {
		t.Fatalf("second Start() error = %v, want already-started error", err)
	}

	if err := process.Wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() error = %v, want context.Canceled", err)
	}
}

func TestProcessWaitAndStopPreserveCleanupFailure(t *testing.T) {
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
			process.state = processStateExited
			process.start.err = startErr
			process.groupErr = wrapStopFailure(cleanupErr)
			close(process.start.done)
			close(process.wait.done)
			for range 2 {
				err = process.Wait()
				if !errors.Is(err, ErrStopFailed) || !errors.Is(err, cleanupErr) {
					t.Fatalf("Wait() error = %v, want stored cleanup failure", err)
				}

				if startErr != nil && !errors.Is(err, startErr) {
					t.Errorf("Wait() error = %v, want start failure", err)
				}

				if err := process.Stop(t.Context()); !errors.Is(err, cleanupErr) {
					t.Errorf("Stop() error = %v, want stored cleanup failure", err)
				}
			}
		})
	}
}

func TestProcessStdoutReportsOutputLimit(t *testing.T) {
	for _, output := range []string{"123", "1234", "12345"} {
		t.Run(output, func(t *testing.T) {
			process, err := New(Spec{Path: "/unused/command", MaxOutputBytes: 4})
			if err != nil {
				t.Fatal(err)
			}

			if _, err := process.output.Write([]byte(output)); err != nil {
				t.Fatal(err)
			}

			// Publish captured output without depending on an OS subprocess.
			process.state = processStateExited
			close(process.start.done)
			close(process.wait.done)
			if err := process.Wait(); err != nil {
				t.Fatalf("Wait() reported an output size error: %v", err)
			}

			got, err := process.Stdout()
			if errors.Is(err, ErrOutputLimitExceeded) != (len(output) > 4) {
				t.Fatalf("Stdout() error = %v for %d bytes, incorrect limit classification", err, len(output))
			}

			if string(got) != output[:min(len(output), 4)] {
				t.Errorf("Stdout() = %q, want retained output prefix", got)
			}
		})
	}
}
