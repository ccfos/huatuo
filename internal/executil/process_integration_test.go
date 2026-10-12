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
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type readyOutput struct {
	once  sync.Once
	ready chan struct{}
}

func (w *readyOutput) Write(data []byte) (int, error) {
	w.once.Do(func() { close(w.ready) })
	return len(data), nil
}

func startReadyProcess(t *testing.T, script string, options ...Option) *Process {
	t.Helper()
	ready := &readyOutput{ready: make(chan struct{})}
	options = append(options, WithStdout(ready))
	process, err := New(Spec{
		Path: "/bin/sh", Args: []string{"-c", script}, StopGracePeriod: 20 * time.Millisecond,
	}, options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := process.Close(); err != nil {
			t.Errorf("Close(): %v", err)
		}
	})
	if err := process.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready.ready:
	case <-process.Done():
		t.Fatalf("command exited before ready: %v", process.Wait())
	case <-time.After(5 * time.Second):
		t.Fatal("command did not become ready")
	}
	return process
}

func TestProcessStopEscalatesAndReaps(t *testing.T) {
	for _, earlyContext := range []bool{false, true} {
		t.Run(fmt.Sprint(earlyContext), func(t *testing.T) {
			process := startReadyProcess(t, `trap '' TERM; printf ready; while :; do sleep 30; done`)
			process.mu.Lock()
			pid := process.pid
			process.mu.Unlock()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if earlyContext {
				cancel()
			}
			started := time.Now()
			if err := process.Stop(ctx); err != nil {
				t.Fatal(err)
			}
			if elapsed := time.Since(started); elapsed > 3*time.Second {
				t.Fatalf("Stop took %s", elapsed)
			}
			if err := process.Wait(); !errors.Is(err, ErrStopped) {
				t.Fatalf("Wait() = %v", err)
			}
			var status syscall.WaitStatus
			if _, err := syscall.Wait4(pid, &status, syscall.WNOHANG, nil); !errors.Is(err, syscall.ECHILD) {
				t.Fatalf("child %d was not reaped: %v", pid, err)
			}
		})
	}
}

func TestProcessExpectedStopReturnsSentinel(t *testing.T) {
	for _, name := range []string{"graceful stop", "force stop", "close"} {
		t.Run(name, func(t *testing.T) {
			script := `printf ready; while :; do sleep 30; done`
			if name == "force stop" {
				script = `trap '' TERM; printf ready; while :; do sleep 30; done`
			}
			process := startReadyProcess(t, script)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if name == "force stop" {
				cancel()
			}
			stop := func() error { return process.Stop(ctx) }
			if name == "close" {
				stop = process.Close
			}
			if err := stop(); err != nil {
				t.Fatalf("stop operation: %v", err)
			}
			if err := process.Close(); err != nil {
				t.Fatalf("Close(): %v", err)
			}
			for _, err := range []error{process.Wait(), process.Wait()} {
				if err != ErrStopped { //nolint:errorlint // Verify the direct-return contract.
					t.Fatalf("lifecycle error = %v, want ErrStopped directly", err)
				}
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					t.Fatalf("expected stop retained its exit status: %v", exitErr)
				}
			}
		})
	}
}

func TestProcessConcurrentStopCloseAndWait(t *testing.T) {
	process := startReadyProcess(t, `printf ready; while :; do sleep 30; done`)
	var group sync.WaitGroup
	for range 8 {
		group.Add(3)
		go func() {
			defer group.Done()
			if err := process.Stop(t.Context()); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer group.Done()
			if err := process.Close(); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer group.Done()
			if err := process.Wait(); !errors.Is(err, ErrStopped) {
				t.Errorf("Wait(): %v", err)
			}
		}()
	}
	group.Wait()
	select {
	case <-process.Done():
	default:
		t.Fatal("Done remains open after Wait")
	}
}

func newGatedProcess(t *testing.T, exitCode int) (*Process, <-chan struct{}, func()) {
	t.Helper()
	spec, options, ready, release := newGatedCommand(t, exitCode)
	process, err := New(spec, options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := process.Close(); err != nil {
			t.Error(err)
		}
	})

	return process, ready, release
}

func newGatedCommand(t *testing.T, exitCode int) (Spec, []Option, <-chan struct{}, func()) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	release := sync.OnceFunc(func() {
		if err := writer.Close(); err != nil {
			t.Error(err)
		}
	})
	t.Cleanup(func() {
		release()
		if err := reader.Close(); err != nil {
			t.Error(err)
		}
	})

	ready := &readyOutput{ready: make(chan struct{})}
	spec := Spec{
		Path:            "/bin/sh",
		Args:            []string{"-c", `printf ready; read -r ignored <&3; exit "$1"`, "executil-test", strconv.Itoa(exitCode)},
		StopGracePeriod: 20 * time.Millisecond,
	}
	options := []Option{WithExtraFiles(reader), WithStdout(ready)}

	return spec, options, ready.ready, release
}

func TestProcessDoneNotifiesAllObservers(t *testing.T) {
	for _, name := range []string{"normal exit", "nonzero exit", "failed start", "close before start", "close while running"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			var process *Process
			var ready <-chan struct{}
			var release func()
			if name == "failed start" || name == "close before start" {
				var err error
				process, err = New(Spec{Path: "/missing/executil-command"})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := process.Close(); err != nil {
						t.Error(err)
					}
				})
			} else {
				exitCode := 0
				if name == "nonzero exit" {
					exitCode = 7
				}
				process, ready, release = newGatedProcess(t, exitCode)
			}

			const observers = 8
			observing := make(chan struct{})
			results := make(chan error)
			var group sync.WaitGroup
			t.Cleanup(func() {
				cancel()
				if err := process.Close(); err != nil {
					t.Error(err)
				}
				group.Wait()
			})
			for range observers {
				group.Add(1)
				go func() {
					defer group.Done()
					select {
					case observing <- struct{}{}:
					case <-ctx.Done():
						return
					}
					select {
					case <-process.Done():
						err := process.Wait()
						select {
						case results <- err:
						case <-ctx.Done():
						}
					case <-ctx.Done():
					}
				}()
			}
			for range observers {
				select {
				case <-observing:
				case <-ctx.Done():
					t.Fatal("Done observers did not become ready")
				}
			}
			select {
			case <-process.Done():
				t.Fatal("Done closed before the process was started or closed")
			default:
			}

			switch name {
			case "failed start":
				if err := process.Start(ctx); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("Start(): %v", err)
				}
			case "close before start":
				if err := process.Close(); err != nil {
					t.Fatal(err)
				}
			default:
				if err := process.Start(ctx); err != nil {
					t.Fatal(err)
				}
				select {
				case <-ready:
				case <-ctx.Done():
					t.Fatal("command did not become ready")
				}
				select {
				case <-process.Done():
					t.Fatal("Done closed while the command was waiting for its exit gate")
				default:
				}
				if name == "close while running" {
					if err := process.Close(); err != nil {
						t.Fatal(err)
					}
				} else {
					release()
				}
			}

			for observer := range observers {
				select {
				case err := <-results:
					var exitErr *exec.ExitError
					valid := err == nil
					switch name {
					case "nonzero exit":
						valid = errors.As(err, &exitErr) && exitErr.ExitCode() == 7
					case "failed start":
						valid = errors.Is(err, os.ErrNotExist)
					case "close before start":
						valid = errors.Is(err, ErrProcessClosed)
					case "close while running":
						valid = errors.Is(err, ErrStopped)
					}
					if !valid {
						t.Errorf("observer %d Wait(): %v", observer, err)
					}
				case <-ctx.Done():
					t.Fatal("Done did not make the final Wait result available to every observer")
				}
			}
		})
	}
}

func TestProcessStartContextDoesNotControlLifetime(t *testing.T) {
	process, err := New(Spec{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := process.Close(); err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithCancel(t.Context())
	if err := process.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := process.Start(ctx); err != ErrProcessAlreadyStarted { //nolint:errorlint // Verify the direct-return contract.
		t.Fatalf("Start() while running = %v, want ErrProcessAlreadyStarted", err)
	}

	cancel()
	select {
	case <-process.Done():
		t.Fatalf("Start context stopped command: %v", process.Wait())
	case <-time.After(20 * time.Millisecond):
	}
}

func TestProcessDonePublishesFailuresAndCloseBeforeStart(t *testing.T) {
	tests := []struct {
		name   string
		finish func(*Process) error
		want   error
	}{
		{"close before start", func(p *Process) error { return p.Close() }, ErrProcessClosed},
		{"failed start", func(p *Process) error { return p.Start(t.Context()) }, os.ErrNotExist},
		{"canceled start", func(p *Process) error {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			return p.Start(ctx)
		}, context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			process, err := New(Spec{Path: "/missing/executil-command"})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-process.Done():
				t.Fatal("Done closed before start")
			default:
			}
			finishErr := tt.finish(process)
			wantFinishErr := tt.want
			if tt.name == "close before start" {
				wantFinishErr = nil
			}
			if !errors.Is(finishErr, wantFinishErr) {
				t.Fatalf("finish: %v", finishErr)
			}
			select {
			case <-process.Done():
			case <-time.After(time.Second):
				t.Fatal("Done was not closed")
			}
			for range 2 {
				if err := process.Wait(); !errors.Is(err, tt.want) {
					t.Fatalf("Wait(): %v", err)
				}
				if err := process.Close(); err != nil {
					t.Fatal(err)
				}
			}
			wantStartErr := ErrProcessAlreadyStarted
			if tt.name == "close before start" {
				wantStartErr = ErrProcessClosed
			}
			if err := process.Start(t.Context()); err != wantStartErr { //nolint:errorlint // Verify the direct-return contract.
				t.Fatalf("Start() after completion = %v, want %v", err, wantStartErr)
			}
		})
	}
	var zero Process
	if zero.Done() != nil {
		t.Fatal("zero Done must be nil")
	}
	if err := zero.Close(); !errors.Is(err, ErrProcessNotInitialized) {
		t.Fatalf("zero Close(): %v", err)
	}
}

func TestProcessStartCanceledDuringPreparation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	process, err := New(Spec{Path: "/missing/executil-command"}, WithMemfdOutput(10, func(string) []string {
		cancel()
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}

	err = process.Start(ctx)
	if !errors.Is(err, context.Canceled) || errors.Is(err, os.ErrNotExist) ||
		errors.Is(err, ErrRunCanceled) || errors.Is(err, ErrRunTimeout) {
		t.Fatalf("Start() error = %v, want cancellation before attempting execution", err)
	}
	if process.memfd.file != nil {
		t.Fatal("canceled Start() retained its memfd")
	}
	if err := process.Wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() error = %v, want context.Canceled", err)
	}
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
}

type cancelAfterStartContext struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (c *cancelAfterStartContext) Err() error {
	c.checks++
	// The third launch check follows cmd.Start; cancellation must be observed
	// before publishing success, independent of child scheduling.
	if c.checks == 3 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestProcessStartCanceledAfterLaunch(t *testing.T) {
	for _, memfd := range []bool{false, true} {
		t.Run(fmt.Sprint(memfd), func(t *testing.T) {
			spec, options, _, _ := newGatedCommand(t, 7)
			var file *os.File
			var process *Process
			if memfd {
				options = append(options, WithMemfdOutput(10, func(string) []string {
					file = process.memfd.file
					return nil
				}))
			}
			var err error
			process, err = New(spec, options...)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := process.Close(); err != nil {
					t.Error(err)
				}
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			launchCtx := &cancelAfterStartContext{Context: ctx, cancel: cancel}
			startErr := process.Start(launchCtx)
			if launchCtx.checks != 3 {
				t.Fatalf("launch context checks = %d, want cancellation after cmd.Start", launchCtx.checks)
			}
			if err := process.Close(); err != nil {
				t.Fatalf("Close(): %v", err)
			}
			for _, err := range []error{startErr, process.Wait()} {
				if err != context.Canceled { //nolint:errorlint // A completed cancellation returns the same launch marker.
					t.Fatalf("lifecycle error = %v, want context.Canceled directly", err)
				}
			}
			if file != nil {
				if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("canceled launch retained its owned file: %v", err)
				}
			}
			select {
			case <-process.Done():
			default:
				t.Fatal("canceled launch did not publish completion")
			}
		})
	}
}

func TestProcessCloseAndWaitDuringCanceledStart(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	preparing, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	process, err := New(Spec{Path: "/missing/executil-command"}, WithMemfdOutput(10, func(string) []string {
		close(preparing)
		<-release
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		unblock()
		if err := process.Close(); err != nil {
			t.Error(err)
		}
	})
	startResult := make(chan error, 1)
	go func() { startResult <- process.Start(ctx) }()
	<-preparing
	file := process.memfd.file
	waitResult := make(chan error, 1)
	go func() { waitResult <- process.Wait() }()
	closeResult := make(chan error, 1)
	go func() { closeResult <- process.Close() }()
	cancel()
	unblock()
	if err := <-closeResult; err != nil {
		t.Fatalf("Close(): %v", err)
	}
	for _, result := range []<-chan error{startResult, waitResult} {
		if err := <-result; err != context.Canceled { //nolint:errorlint // All callers must observe the same launch marker.
			t.Fatalf("lifecycle error = %v, want context.Canceled directly", err)
		}
	}
	if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("canceled launch retained its owned file: %v", err)
	}
	select {
	case <-process.Done():
	default:
		t.Fatal("canceled launch did not publish completion")
	}
}

func TestProcessCloseAndWaitDuringStart(t *testing.T) {
	preparing, release := make(chan struct{}), make(chan struct{})
	process, err := New(Spec{Path: "/bin/sh"}, WithMemfdOutput(10, func(string) []string {
		close(preparing)
		<-release
		return []string{"-c", "sleep 30"}
	}))
	if err != nil {
		t.Fatal(err)
	}
	startResult := make(chan error, 1)
	go func() { startResult <- process.Start(t.Context()) }()
	<-preparing
	if err := process.Start(t.Context()); err != ErrProcessAlreadyStarted { //nolint:errorlint // Verify the direct-return contract.
		t.Errorf("Start() during launch = %v, want ErrProcessAlreadyStarted", err)
	}

	waitResult := make(chan error, 1)
	go func() { waitResult <- process.Wait() }()
	closeResult := make(chan error, 1)
	go func() { closeResult <- process.Close() }()
	close(release)
	if err := <-startResult; err != nil {
		t.Fatal(err)
	}
	if err := <-closeResult; err != nil {
		t.Fatal(err)
	}
	if err := <-waitResult; !errors.Is(err, ErrStopped) {
		t.Fatalf("Wait(): %v", err)
	}
	if _, err := process.MemfdOutput(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("MemfdOutput(): %v", err)
	}
}

func TestProcessLeaderExitKillsRemainingGroup(t *testing.T) {
	process, err := New(Spec{Path: "/bin/sh", Args: []string{"-c", `sleep 30 & printf '%s' "$!"`}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := process.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := process.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := process.Wait(); err != nil {
		t.Fatal(err)
	}
	output := process.Stdout()
	pid, err := strconv.Atoi(string(output))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		state, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		fields := strings.Fields(string(state)[strings.LastIndex(string(state), ")")+1:])
		if len(fields) > 0 && fields[0] == "Z" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("group member %d is still running", pid)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestProcessReportsUnclosedOutputPipe(t *testing.T) {
	// setsid deliberately escapes the managed group and keeps stdout open.
	process, err := New(Spec{Path: "/bin/sh", Args: []string{"-c", `setsid /bin/sh -c 'touch "$1"; sleep 30' escaped "$1" & echo "$!"; while [ ! -e "$1" ]; do sleep 0.01; done`, "runner", filepath.Join(t.TempDir(), "ready")}})
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	err = process.Wait()
	output := process.Stdout()
	fields := strings.Fields(string(output))
	if len(fields) == 0 {
		t.Fatalf("no escaped pid: %v", err)
	}
	pid, parseErr := strconv.Atoi(fields[0])
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	if killErr := syscall.Kill(-pid, syscall.SIGKILL); killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
		t.Error(killErr)
	}
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("Run(): %v, want ErrWaitDelay", err)
	}
	if err := process.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
}

func TestProcessTerminalErrorIsSharedAndUnchangedByRejectedStart(t *testing.T) {
	process, ready, release := newGatedProcess(t, 7)
	if err := process.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-ready
	if err := process.Start(t.Context()); !errors.Is(err, ErrProcessAlreadyStarted) {
		t.Fatalf("repeated Start(): %v", err)
	}
	release()
	terminalErr := process.Wait()
	var exitErr *exec.ExitError
	if !errors.As(terminalErr, &exitErr) || exitErr.ExitCode() != 7 || errors.Is(terminalErr, ErrProcessAlreadyStarted) {
		t.Fatalf("Wait(): %v", terminalErr)
	}
	for _, err := range []error{process.Stop(t.Context()), process.Close()} {
		if err != nil {
			t.Fatalf("cleanup repeated execution failure: %v", err)
		}
	}
	for _, operation := range []func() error{process.Wait, process.Wait} {
		if got := operation(); got != terminalErr { //nolint:errorlint // Verify all methods reuse the published error object.
			t.Fatalf("operation returned %v, want the published error %v", got, terminalErr)
		}
	}
	if errors.Is(terminalErr, ErrCleanupFailed) {
		t.Fatalf("ordinary exit was classified as cleanup failure: %v", terminalErr)
	}
}

func TestProcessExternalSignalIsNotExpectedStop(t *testing.T) {
	process := startReadyProcess(t, `printf ready; while :; do sleep 30; done`)
	process.mu.Lock()
	pid := process.pid
	process.mu.Unlock()
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	terminalErr := process.Wait()
	var exitErr *exec.ExitError
	if errors.Is(terminalErr, ErrStopped) || !errors.As(terminalErr, &exitErr) {
		t.Fatalf("external signal result: %v", terminalErr)
	}
}
