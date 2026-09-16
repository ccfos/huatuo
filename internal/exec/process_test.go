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

//go:build linux

package exec

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	helperModeEnv  = "HUATUO_EXEC_HELPER_MODE"
	helperValueEnv = "HUATUO_EXEC_HELPER_VALUE"
	helperReady    = "helper-ready"
)

var errTestSignal = errors.New("test signal failed")

func TestNewValidatesAndSnapshotsSpec(t *testing.T) {
	tests := []struct {
		name    string
		spec    Spec
		wantErr string
	}{
		{
			name:    "missing path",
			spec:    Spec{},
			wantErr: "path must not be empty",
		},
		{
			name:    "blank path",
			spec:    Spec{Path: "  \t"},
			wantErr: "path must not be empty",
		},
		{
			name:    "null byte in path",
			spec:    Spec{Path: "bad\x00path"},
			wantErr: "path \"bad\\x00path\" contains a null byte",
		},
		{
			name:    "null byte in argument",
			spec:    Spec{Path: "/bin/true", Args: []string{"bad\x00arg"}},
			wantErr: "argument 0 contains a null byte",
		},
		{
			name:    "null byte in environment",
			spec:    Spec{Path: "/bin/true", Env: []string{"KEY=bad\x00value"}},
			wantErr: "environment entry 0 contains a null byte",
		},
		{
			name:    "invalid environment",
			spec:    Spec{Path: "/bin/true", Env: []string{"INVALID"}},
			wantErr: "environment entry 0 must use a non-empty KEY=VALUE form",
		},
		{
			name:    "empty environment key",
			spec:    Spec{Path: "/bin/true", Env: []string{"=value"}},
			wantErr: "environment entry 0 must use a non-empty KEY=VALUE form",
		},
		{
			name:    "negative stop grace period",
			spec:    Spec{Path: "/bin/true", StopGracePeriod: -time.Second},
			wantErr: "stop grace period must not be negative",
		},
		{
			name:    "negative output limit",
			spec:    Spec{Path: "/bin/true", MaxOutputBytes: -1},
			wantErr: "maximum output bytes must not be negative",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.spec)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Errorf("New() error = %v, want substring %q", err, test.wantErr)
			}
		})
	}

	args := []string{"first"}
	env := []string{"KEY=value"}
	process, err := New(Spec{Path: "/bin/true", Args: args, Env: env})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	args[0] = "mutated"
	env[0] = "KEY=mutated"
	if process.spec.Args[0] != "first" {
		t.Errorf("snapshotted argument = %q, want first", process.spec.Args[0])
	}
	if process.spec.Env[0] != "KEY=value" {
		t.Errorf("snapshotted environment = %q, want KEY=value", process.spec.Env[0])
	}
	if process.spec.StopGracePeriod != defaultStopGracePeriod {
		t.Errorf("StopGracePeriod = %s, want %s", process.spec.StopGracePeriod, defaultStopGracePeriod)
	}
	if process.spec.MaxOutputBytes != defaultMaxOutputBytes {
		t.Errorf("MaxOutputBytes = %d, want %d", process.spec.MaxOutputBytes, defaultMaxOutputBytes)
	}
	emptyEnvProcess, err := New(Spec{Path: "/bin/true", Env: []string{}})
	if err != nil {
		t.Fatalf("New() with empty environment error = %v", err)
	}
	if emptyEnvProcess.spec.Env == nil {
		t.Error("New() changed an explicit empty environment to inherited environment")
	}
}

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
	if err := process.Start(context.Background()); err == nil {
		t.Fatal("Start() error = nil, want missing executable error")
	}
	if err := process.Wait(); err == nil {
		t.Fatal("Wait() after failed Start error = nil, want start error")
	}
	if err := process.Stop(ctx); err != nil {
		t.Errorf("Stop() after failed Start error = %v", err)
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
	if got := process.Stdout(); len(got) != 0 {
		t.Errorf("Stdout() = %q, want empty output", got)
	}
	if got := process.Stderr(); len(got) != 0 {
		t.Errorf("Stderr() = %q, want empty output", got)
	}
}

func TestProcessStartRejectsCanceledContextAndRetry(t *testing.T) {
	process := newHelperProcess(t, "output")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := process.Start(ctx)
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

func TestProcessWaitDuringCanceledStartReturnsStartError(t *testing.T) {
	process := newHelperProcess(t, "output")
	realForceStop := process.forceStop
	forceStarted := make(chan struct{})
	releaseForce := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(releaseForce)
		})
	}
	t.Cleanup(release)
	process.forceStop = func(pid int) error {
		close(forceStarted)
		<-releaseForce
		return realForceStop(pid)
	}

	startResult := make(chan error, 1)
	ctx := cancelContextAfterStart(process)
	go func() {
		startResult <- process.Start(ctx)
	}()
	select {
	case <-forceStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for canceled start cleanup")
	}

	waitResult := make(chan error, 1)
	go func() {
		waitResult <- process.Wait()
	}()
	select {
	case err := <-waitResult:
		t.Fatalf("Wait() returned before Start() finalized: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	release()
	if err := receiveError(t, startResult); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() error = %v, want context.Canceled", err)
	}
	if err := receiveError(t, waitResult); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() error = %v, want context.Canceled", err)
	}
}

func TestProcessCanceledStartDoesNotWaitAfterForceStopFailure(t *testing.T) {
	process := newHelperProcess(t, "ignore-term")
	realForceStop := process.forceStop
	t.Cleanup(func() {
		process.forceStop = realForceStop
	})
	process.forceStop = func(_ int) error {
		return errTestSignal
	}

	startResult := make(chan error, 1)
	ctx := cancelContextAfterStart(process)
	go func() {
		startResult <- process.Start(ctx)
	}()
	err := receiveError(t, startResult)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Start() error = %v, want context.Canceled", err)
	}
	if !errors.Is(err, errTestSignal) {
		t.Errorf("Start() error = %v, want force-stop failure", err)
	}
	if !errors.Is(err, ErrStopFailed) {
		t.Errorf("Start() error = %v, want ErrStopFailed", err)
	}

	process.forceStop = realForceStop
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := process.Stop(ctx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := process.Wait(); !errors.Is(err, context.Canceled) {
		t.Errorf("Wait() error = %v, want canceled Start result", err)
	}
}

func TestProcessReportsOutputOverflowAfterReaping(t *testing.T) {
	process := newHelperProcessWithSpec(t, "large-output", &Spec{MaxOutputBytes: 1024})
	startProcess(t, process)
	err := process.Wait()
	if err == nil || !strings.Contains(err.Error(), "stdout exceeds 1024 bytes") {
		t.Fatalf("Wait() error = %v, want stdout limit error", err)
	}

	payload := largeOutputPayload()
	want := payload[:1024]
	if got := string(process.Stdout()); got != want {
		t.Errorf("Stdout() length = %d, want %d", len(got), len(want))
	}
}

func TestProcessRetainsFixedErrorTail(t *testing.T) {
	process := newHelperProcess(t, "large-error")
	startProcess(t, process)
	if err := process.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}

	payload := largeOutputPayload()
	want := payload[len(payload)-maxErrorOutputBytes:]
	if got := string(process.Stderr()); got != want {
		t.Errorf("Stderr() length = %d, want %d", len(got), len(want))
	}
}

func TestProcessSeparatesStdoutFromStderr(t *testing.T) {
	process := newHelperProcess(t, "split-output")
	startProcess(t, process)
	if err := process.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if got := string(process.Stdout()); got != "payload" {
		t.Errorf("Stdout() = %q, want payload", got)
	}
	if got := string(process.Stderr()); got != "diagnostic" {
		t.Errorf("Stderr() = %q, want diagnostic", got)
	}
}

func TestProcessStdoutAndStderrReturnCopies(t *testing.T) {
	process := newHelperProcess(t, "split-output")
	startProcess(t, process)
	if err := process.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	output := process.Stdout()
	stderr := process.Stderr()
	output[0] = 'x'
	stderr[0] = 'x'
	if got := string(process.Stdout()); got != "payload" {
		t.Errorf("Stdout() after caller mutation = %q, want payload", got)
	}
	if got := string(process.Stderr()); got != "diagnostic" {
		t.Errorf("Stderr() after caller mutation = %q, want diagnostic", got)
	}
}

func TestProcessInheritsEnvironment(t *testing.T) {
	t.Setenv(helperModeEnv, "print-env")
	t.Setenv(helperValueEnv, "inherited")

	process, err := New(Spec{
		Path: os.Args[0],
		Args: []string{"-test.run=^TestExecHelperProcess$"},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	startProcess(t, process)
	if err := process.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if got := string(process.Stdout()); got != "inherited" {
		t.Errorf("stdout = %q, want inherited environment value", got)
	}
}

func TestProcessReportsExecutionFailure(t *testing.T) {
	process := newHelperProcess(t, "fail")
	startProcess(t, process)

	err := process.Wait()
	if err == nil {
		t.Fatal("Wait() error = nil, want exit failure")
	}
	if errors.Is(err, ErrStopped) {
		t.Fatalf("Wait() error = %v, do not want ErrStopped", err)
	}
	if !strings.Contains(err.Error(), "exit status") {
		t.Errorf("Wait() error = %v, want exit status", err)
	}
	if got := string(process.Stderr()); got != "helper failed" {
		t.Errorf("Stderr() = %q, want failure diagnostic", got)
	}
}

func TestProcessGracefulStopAndRepeatedStop(t *testing.T) {
	process := newHelperProcess(t, "graceful")
	startProcess(t, process)
	waitForStdout(t, process, helperReady)

	var waitGroup sync.WaitGroup
	stopResults := make(chan error, 2)
	for range 2 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			stopResults <- process.Stop(ctx)
		}()
	}
	waitGroup.Wait()
	close(stopResults)
	for err := range stopResults {
		if err != nil {
			t.Errorf("Stop() error = %v", err)
		}
	}
	if err := process.Wait(); err != nil {
		t.Errorf("Wait() error = %v, want nil for graceful exit", err)
	}
	if !strings.Contains(string(process.Stdout()), "helper-terminated") {
		t.Errorf("Stdout() = %q, want graceful termination marker", process.Stdout())
	}
}

func TestProcessStopEscalatesToForce(t *testing.T) {
	process := newHelperProcess(t, "ignore-term")
	startProcess(t, process)
	waitForStdout(t, process, helperReady)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := process.Stop(ctx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := process.Wait(); !errors.Is(err, ErrStopped) {
		t.Errorf("Wait() error = %v, want ErrStopped", err)
	}
}

func TestProcessStopTerminatesProcessGroup(t *testing.T) {
	process := newHelperProcess(t, "process-group")
	startProcess(t, process)
	waitForStdout(t, process, helperReady)
	childPID := childPIDFromStdout(t, string(process.Stdout()))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := process.Stop(ctx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := process.Wait(); err != nil && !errors.Is(err, ErrStopped) {
		t.Errorf("Wait() error = %v, want graceful or forced stop", err)
	}
	waitForProcessExit(t, childPID)
}

func TestProcessStopAfterExitIsSuccessful(t *testing.T) {
	process := newHelperProcess(t, "output")
	startProcess(t, process)
	if err := process.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if err := process.Wait(); err != nil {
		t.Fatalf("second Wait() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := process.Stop(ctx); err != nil {
		t.Errorf("Stop() after Wait error = %v", err)
	}
}

func TestProcessConcurrentWaitReturnsSameResult(t *testing.T) {
	process := newHelperProcess(t, "fail")
	startProcess(t, process)

	results := make(chan error, 4)
	var waitGroup sync.WaitGroup
	for range 4 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			results <- process.Wait()
		}()
	}
	waitGroup.Wait()
	close(results)
	for err := range results {
		if err == nil || !strings.Contains(err.Error(), "exit status 7") {
			t.Errorf("Wait() error = %v, want exit status 7", err)
		}
	}
}

func TestProcessStartContextDoesNotOwnLifetime(t *testing.T) {
	process := newHelperProcess(t, "graceful")
	ctx, cancel := context.WithCancel(context.Background())
	if err := process.Start(ctx); err != nil {
		cancel()
		t.Fatalf("Start() error = %v", err)
	}
	cancel()
	waitForStdout(t, process, helperReady)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopCancel()
	if err := process.Stop(stopCtx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := process.Wait(); err != nil {
		t.Errorf("Wait() error = %v", err)
	}
}

func TestProcessRunWaitsForNaturalExit(t *testing.T) {
	process := newHelperProcess(t, "output")
	if err := process.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := string(process.Stdout()); got != "output" {
		t.Errorf("Stdout() = %q, want output", got)
	}
}

func TestProcessRunStopsAfterCancellation(t *testing.T) {
	process := newHelperProcess(t, "graceful")
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- process.Run(ctx)
	}()
	waitForStdout(t, process, helperReady)
	cancel()

	err := receiveError(t, result)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrStopFailed) {
		t.Fatalf("Run() error = %v, do not want ErrStopFailed", err)
	}
	if err := process.Wait(); err != nil {
		t.Errorf("Wait() error = %v, want graceful exit", err)
	}
}

func TestProcessRunEscalatesAfterGracePeriod(t *testing.T) {
	process := newHelperProcessWithSpec(t, "ignore-term", &Spec{
		StopGracePeriod: 50 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- process.Run(ctx)
	}()
	waitForStdout(t, process, helperReady)
	cancel()

	err := receiveError(t, result)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrStopFailed) {
		t.Fatalf("Run() error = %v, do not want ErrStopFailed", err)
	}
	if err := process.Wait(); !errors.Is(err, ErrStopped) {
		t.Errorf("Wait() error = %v, want ErrStopped", err)
	}
}

func TestProcessRunReturnsAfterStopFailureAndAllowsRetry(t *testing.T) {
	process := newHelperProcessWithSpec(t, "ignore-term", &Spec{
		StopGracePeriod: 50 * time.Millisecond,
	})
	realForceStop := process.forceStop
	forceCalled := make(chan struct{})
	process.forceStop = func(_ int) error {
		close(forceCalled)
		return errTestSignal
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- process.Run(ctx)
	}()
	waitForStdout(t, process, helperReady)
	cancel()
	select {
	case <-forceCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for failed force stop")
	}
	process.forceStop = realForceStop

	err := receiveError(t, result)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Run() error = %v, want context.Canceled", err)
	}
	if !errors.Is(err, errTestSignal) {
		t.Errorf("Run() error = %v, want force-stop failure", err)
	}
	if !errors.Is(err, ErrStopFailed) {
		t.Errorf("Run() error = %v, want ErrStopFailed", err)
	}

	if err := process.Stop(ctx); err != nil {
		t.Fatalf("retry Stop() error = %v", err)
	}
	if err := process.Wait(); !errors.Is(err, ErrStopped) {
		t.Errorf("Wait() error = %v, want ErrStopped", err)
	}
}

func TestExecHelperProcess(t *testing.T) {
	mode := os.Getenv(helperModeEnv)
	if mode == "" {
		return
	}

	switch mode {
	case "output":
		_, _ = fmt.Fprint(os.Stdout, "output")
		os.Exit(0)
	case "large-output":
		_, _ = fmt.Fprint(os.Stdout, largeOutputPayload())
		os.Exit(0)
	case "large-error":
		_, _ = fmt.Fprint(os.Stderr, largeOutputPayload())
		os.Exit(0)
	case "split-output":
		_, _ = fmt.Fprint(os.Stdout, "payload")
		_, _ = fmt.Fprint(os.Stderr, "diagnostic")
		os.Exit(0)
	case "print-env":
		_, _ = fmt.Fprint(os.Stdout, os.Getenv(helperValueEnv))
		os.Exit(0)
	case "fail":
		_, _ = fmt.Fprint(os.Stderr, "helper failed")
		os.Exit(7)
	case "graceful":
		termCh := make(chan os.Signal, 1)
		signal.Notify(termCh, syscall.SIGTERM)
		_, _ = fmt.Fprintln(os.Stdout, helperReady)
		<-termCh
		_, _ = fmt.Fprintln(os.Stdout, "helper-terminated")
		os.Exit(0)
	case "ignore-term":
		signal.Ignore(syscall.SIGTERM)
		_, _ = fmt.Fprintln(os.Stdout, helperReady)
		for {
			time.Sleep(time.Hour)
		}
	case "process-group":
		runProcessGroupHelper()
		os.Exit(0)
	default:
		_, _ = fmt.Fprintf(os.Stderr, "unknown helper mode %q", mode)
		os.Exit(2)
	}
}

func runProcessGroupHelper() {
	termCh := make(chan os.Signal, 1)
	signal.Notify(termCh, syscall.SIGTERM)
	child := osexec.Command("/bin/sh", "-c", "trap 'exit 0' TERM; echo child-ready; while :; do sleep 10; done")
	stdout, err := child.StdoutPipe()
	if err != nil {
		os.Exit(3)
	}
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		os.Exit(4)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "child-ready" {
		_ = child.Process.Kill()
		_, _ = fmt.Fprintln(os.Stderr, "child did not become ready")
		os.Exit(5)
	}
	_, _ = fmt.Fprintf(os.Stdout, "%s child=%d\n", helperReady, child.Process.Pid)
	<-termCh
	if err := child.Wait(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "wait for child: %v", err)
		os.Exit(6)
	}
}

func largeOutputPayload() string {
	return strings.Repeat("0123456789", 7000)
}

func newHelperProcess(t *testing.T, mode string) *Process {
	t.Helper()
	return newHelperProcessWithSpec(t, mode, &Spec{})
}

func newHelperProcessWithSpec(t *testing.T, mode string, source *Spec) *Process {
	t.Helper()
	env := append(os.Environ(), helperModeEnv+"="+mode)
	spec := *source
	spec.Path = os.Args[0]
	spec.Args = []string{"-test.run=^TestExecHelperProcess$"}
	spec.Env = env
	process, err := New(spec)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		_ = process.Stop(ctx)
		_ = process.Wait()
	})
	return process
}

func cancelContextAfterStart(process *Process) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	startCommand := process.startCommand
	process.startCommand = func(cmd *osexec.Cmd) error {
		err := startCommand(cmd)
		if err == nil {
			cancel()
		}
		return err
	}
	return ctx
}

func startProcess(t *testing.T, process *Process) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := process.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v; output=%q; stderr=%q", err, process.Stdout(), process.Stderr())
	}
}

func waitForStdout(t *testing.T, process *Process, marker string) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		if strings.Contains(string(process.Stdout()), marker) {
			return
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatalf("timed out waiting for output %q; output=%q", marker, process.Stdout())
		}
	}
}

func receiveError(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for process result")
		return nil
	}
}

func childPIDFromStdout(t *testing.T, output string) int {
	t.Helper()
	index := strings.Index(output, "child=")
	if index < 0 {
		t.Fatalf("output = %q, want child PID", output)
	}
	value := strings.Fields(output[index+len("child="):])[0]
	pid, err := strconv.Atoi(value)
	if err != nil {
		t.Fatalf("parse child PID %q: %v", value, err)
	}
	return pid
}

func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil {
			t.Fatalf("check child PID %d: %v", pid, err)
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatalf("child PID %d still exists", pid)
		}
	}
}
