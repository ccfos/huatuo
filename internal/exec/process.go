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

// Package exec starts and manages operating-system processes.
package exec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"slices"
	"strings"
	"sync"
	"time"
)

// ErrStopped reports that Stop terminated the command with a signal.
var ErrStopped = errors.New("exec: command stopped")

// ErrStopFailed reports that context-triggered process termination failed.
var ErrStopFailed = errors.New("exec: stop failed")

var errProcessNotInitialized = errors.New("process is not initialized")

const (
	defaultStopGracePeriod = 5 * time.Second
	defaultMaxOutputBytes  = 64 << 10
)

// Spec describes one external command invocation.
type Spec struct {
	Path string
	Args []string
	// Env replaces the child environment. A nil Env inherits the parent environment.
	Env []string
	// StopGracePeriod controls when Run escalates from SIGTERM to SIGKILL.
	// A zero value uses five seconds.
	StopGracePeriod time.Duration
	// MaxOutputBytes limits retained standard output. A zero value uses 64 KiB.
	MaxOutputBytes int
}

type processState uint8

const (
	processStateInvalid processState = iota
	processStateNew
	processStateStarting
	processStateRunning
	processStateExited
	processStateStartFailed
)

// Closing done publishes the immutable error to every waiter.
type lifecycleResult struct {
	done chan struct{}
	err  error
}

// Process owns one command lifecycle. It must be created with New and cannot be
// restarted; methods on its zero value return an initialization error.
type Process struct {
	spec   Spec
	output outputBuffer
	stderr tailBuffer

	mu           sync.Mutex
	state        processState
	pid          int
	start        lifecycleResult
	wait         lifecycleResult
	stopAttempt  *lifecycleResult
	startCommand func(*osexec.Cmd) error
	forceStop    func(int) error

	groupMu sync.Mutex
	// groupMu serializes the first stop signal with the reaper's decision
	// to release the leader. A failed signal can be retried while retained.
	groupReleased   bool
	groupStopping   bool
	leaderExited    chan struct{}
	releaseLeader   chan struct{}
	leaderErr       error
	groupRunning    func(int) (bool, error)
	groupCleanupErr error
	outputReaders   [2]*os.File
}

// New validates and snapshots a command specification without starting it.
func New(spec Spec) (*Process, error) { //nolint:gocritic // Spec is at the project's 80-byte value limit.
	if err := spec.validate(); err != nil {
		return nil, fmt.Errorf("new command: %w", err)
	}
	if spec.StopGracePeriod == 0 {
		spec.StopGracePeriod = defaultStopGracePeriod
	}
	if spec.MaxOutputBytes == 0 {
		spec.MaxOutputBytes = defaultMaxOutputBytes
	}

	spec.Args = slices.Clone(spec.Args)
	spec.Env = slices.Clone(spec.Env)
	return &Process{
		spec:          spec,
		output:        newOutputBuffer(spec.MaxOutputBytes),
		state:         processStateNew,
		start:         lifecycleResult{done: make(chan struct{})},
		wait:          lifecycleResult{done: make(chan struct{})},
		startCommand:  (*osexec.Cmd).Start,
		forceStop:     forceStopProcessGroup,
		leaderExited:  make(chan struct{}),
		releaseLeader: make(chan struct{}),
		groupRunning:  processGroupRunning,
	}, nil
}

func (s *Spec) validate() error {
	if strings.TrimSpace(s.Path) == "" {
		return errors.New("command path must not be empty")
	}
	if strings.IndexByte(s.Path, 0) >= 0 {
		return fmt.Errorf("command path %q contains a null byte", s.Path)
	}
	for index, arg := range s.Args {
		if strings.IndexByte(arg, 0) >= 0 {
			return fmt.Errorf("command argument %d contains a null byte", index)
		}
	}
	for index, value := range s.Env {
		if strings.IndexByte(value, 0) >= 0 {
			return fmt.Errorf("command environment entry %d contains a null byte", index)
		}
		if strings.IndexByte(value, '=') <= 0 {
			return fmt.Errorf(
				"command environment entry %d must use a non-empty KEY=VALUE form",
				index,
			)
		}
	}
	if s.StopGracePeriod < 0 {
		return errors.New("stop grace period must not be negative")
	}
	if s.MaxOutputBytes < 0 {
		return errors.New("maximum output bytes must not be negative")
	}
	return nil
}

// Start launches the command and starts its single internal reaper.
// The context controls launch only; canceling it after Start returns does not
// stop the process.
func (p *Process) Start(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("start command %q: context must not be nil", p.spec.Path)
	}

	p.mu.Lock()
	if p.state == processStateInvalid {
		p.mu.Unlock()
		return fmt.Errorf("start command: %w", errProcessNotInitialized)
	}
	if p.state != processStateNew {
		p.mu.Unlock()
		return fmt.Errorf("start command %q: process has already been started", p.spec.Path)
	}
	p.state = processStateStarting
	p.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return p.failStart(fmt.Errorf("start command %q: %w", p.spec.Path, err))
	}

	cmd := osexec.Command(p.spec.Path, p.spec.Args...)
	cmd.Env = p.spec.Env

	configureCommand(cmd)
	outputDone, err := p.startWithOutput(cmd)
	if err != nil {
		return p.failStart(fmt.Errorf("start command %q: %w", p.spec.Path, err))
	}

	p.mu.Lock()
	p.pid = cmd.Process.Pid
	launchErr := ctx.Err()
	if launchErr != nil {
		// A canceled launch owns cleanup before its reaper can run.
		p.groupStopping = true
	}
	go p.reap(cmd, outputDone)
	if launchErr == nil {
		p.state = processStateRunning
		close(p.start.done)
	}
	p.mu.Unlock()

	if launchErr == nil {
		return nil
	}
	return p.finishCanceledStart(launchErr, cmd.Process.Pid)
}

func (p *Process) startWithOutput(cmd *osexec.Cmd) (<-chan error, error) {
	stdout, stdoutWriter, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create stdout pipe: %w", err)
	}
	defer func() { _ = stdoutWriter.Close() }()
	stderr, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdout.Close()
		return nil, fmt.Errorf("create stderr pipe: %w", err)
	}
	defer func() { _ = stderrWriter.Close() }()

	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter
	if err := p.startCommand(cmd); err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, err
	}
	// Own the readers so output completion can be observed before cmd.Wait
	// releases the leader's PID. Descendants may still hold either pipe open.
	done := make(chan error, 2)
	p.outputReaders = [2]*os.File{stdout, stderr}
	go copyCommandOutput(&p.output, stdout, done)
	go copyCommandOutput(&p.stderr, stderr, done)
	return done, nil
}

func copyCommandOutput(dst io.Writer, src *os.File, done chan<- error) {
	_, err := io.Copy(dst, src)
	_ = src.Close()
	done <- err
}

func (p *Process) finishCanceledStart(launchErr error, pid int) error {
	forceErr := wrapStopFailure(p.forceStopAndWait(pid, p.wait.done))

	p.mu.Lock()
	var waitErr error
	if forceErr == nil && !errors.Is(p.wait.err, ErrStopped) {
		waitErr = p.wait.err
	}
	p.start.err = errors.Join(
		fmt.Errorf("start command %q: %w", p.spec.Path, launchErr),
		forceErr,
		waitErr,
	)
	if forceErr == nil {
		p.state = processStateStartFailed
	} else {
		select {
		case <-p.wait.done:
			p.state = processStateExited
		default:
			p.state = processStateRunning
		}
	}
	close(p.start.done)
	startErr := p.start.err
	p.mu.Unlock()
	return startErr
}

func (p *Process) failStart(err error) error {
	p.mu.Lock()
	p.state = processStateStartFailed
	p.start.err = err
	p.wait.err = err
	close(p.start.done)
	close(p.wait.done)
	p.mu.Unlock()
	return err
}

func (p *Process) reap(cmd *osexec.Cmd, outputDone <-chan error) {
	leaderErr := waitForLeader(cmd.Process.Pid)
	p.groupMu.Lock()
	p.leaderErr = leaderErr
	if leaderErr != nil {
		p.groupReleased = true
	}
	close(p.leaderExited)
	p.groupMu.Unlock()

	outputErr := errors.Join(<-outputDone, <-outputDone)
	p.groupMu.Lock()
	stopping := p.groupStopping
	retain := stopping && leaderErr == nil
	if !retain {
		p.groupReleased = true
	}
	p.groupMu.Unlock()
	if retain {
		<-p.releaseLeader
	}
	p.groupMu.Lock()
	cleanupErr := p.groupCleanupErr
	p.groupMu.Unlock()
	err := cmd.Wait()

	p.mu.Lock()
	if stopping && isStoppedExit(err) {
		err = fmt.Errorf("%w: command %q exited after a stop signal: %w", ErrStopped, p.spec.Path, err)
	} else if err != nil {
		err = fmt.Errorf("wait for command %q: %w", p.spec.Path, err)
	}
	p.wait.err = errors.Join(err, leaderErr, outputErr, cleanupErr)
	if p.state == processStateRunning {
		p.state = processStateExited
	}
	close(p.wait.done)
	p.mu.Unlock()
}

// Wait waits for an in-progress Start and then for the command reaper. If Stop
// has claimed the group, reaping waits for cleanup or a terminal cleanup error.
// Multiple callers receive the same stored result.
func (p *Process) Wait() error {
	for {
		p.mu.Lock()
		if p.state == processStateInvalid {
			p.mu.Unlock()
			return fmt.Errorf("wait for command: %w", errProcessNotInitialized)
		}
		switch p.state {
		case processStateNew:
			p.mu.Unlock()
			return fmt.Errorf("wait for command %q: process has not been started", p.spec.Path)
		case processStateStarting:
			done := p.start.done
			p.mu.Unlock()
			<-done
		case processStateStartFailed:
			err := p.start.err
			p.mu.Unlock()
			return err
		case processStateRunning, processStateExited:
			done := p.wait.done
			p.mu.Unlock()
			<-done
			return p.waitResult()
		default:
			p.mu.Unlock()
			return fmt.Errorf("wait for command %q: invalid process state", p.spec.Path)
		}
	}
}

func (p *Process) waitResult() error {
	return errors.Join(p.processResult(), p.outputError())
}

func (p *Process) processResult() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.start.err != nil {
		return p.start.err
	}
	return p.wait.err
}

func (p *Process) outputError() error {
	if !p.output.Exceeded() {
		return nil
	}
	return fmt.Errorf(
		"command %q stdout exceeds %d bytes",
		p.spec.Path,
		p.spec.MaxOutputBytes,
	)
}

// Stop sends SIGTERM to the process group and waits until ctx expires before
// escalating to SIGKILL. Process reaping remains internal to Process.
func (p *Process) Stop(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("stop command %q: context must not be nil", p.spec.Path)
	}

	p.mu.Lock()
	if p.state == processStateInvalid {
		p.mu.Unlock()
		return fmt.Errorf("stop command: %w", errProcessNotInitialized)
	}
	switch p.state {
	case processStateNew, processStateStarting:
		p.mu.Unlock()
		return fmt.Errorf("stop command %q: process has not been started", p.spec.Path)
	case processStateStartFailed:
		p.mu.Unlock()
		return nil
	}
	if attempt := p.stopAttempt; attempt != nil {
		select {
		case <-attempt.done:
			if attempt.err == nil {
				p.mu.Unlock()
				return nil
			}
		default:
			p.mu.Unlock()
			return p.waitForStopAttempt(ctx, attempt)
		}
	}
	if p.state == processStateExited {
		p.mu.Unlock()
		p.groupMu.Lock()
		err := p.groupCleanupErr
		p.groupMu.Unlock()
		return err
	}

	attempt := &lifecycleResult{done: make(chan struct{})}
	p.stopAttempt = attempt
	pid := p.pid
	waitDone := p.wait.done
	p.mu.Unlock()

	err := p.stopProcessGroup(ctx, pid, waitDone)

	p.mu.Lock()
	attempt.err = err
	close(attempt.done)
	p.mu.Unlock()
	return err
}

func (p *Process) waitForStopAttempt(ctx context.Context, attempt *lifecycleResult) error {
	select {
	case <-attempt.done:
		return attempt.err
	case <-ctx.Done():
		return fmt.Errorf("wait for command %q stop: %w", p.spec.Path, ctx.Err())
	}
}

func (p *Process) stopProcessGroup(ctx context.Context, pid int, waitDone <-chan struct{}) error {
	gracefulErr := p.signalProcessGroup(pid, gracefulStopProcessGroup)
	if processGroupMissing(gracefulErr) {
		gracefulErr = nil
	}
	if gracefulErr != nil {
		forceErr := p.forceStopAndWait(pid, waitDone)
		if forceErr != nil {
			return errors.Join(
				wrapSignalError("gracefully stop", p.spec.Path, gracefulErr),
				forceErr,
			)
		}
		return nil
	}

	if err := p.waitForStoppedGroup(ctx, pid); err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return p.forceStopAndWait(pid, waitDone)
	}
	<-waitDone
	return nil
}

func (p *Process) forceStopAndWait(pid int, waitDone <-chan struct{}) error {
	err := p.signalProcessGroup(pid, p.forceStop)
	if processGroupMissing(err) {
		err = nil
	}
	if err != nil {
		return wrapSignalError("force stop", p.spec.Path, err)
	}
	if err := p.waitForStoppedGroup(context.Background(), pid); err != nil {
		return err
	}
	<-waitDone
	return nil
}

func (p *Process) signalProcessGroup(pid int, signal func(int) error) error {
	p.groupMu.Lock()
	defer p.groupMu.Unlock()
	if p.groupReleased {
		return p.groupCleanupErr
	}
	p.groupStopping = true
	return signal(pid)
}

func (p *Process) waitForStoppedGroup(ctx context.Context, pid int) error {
	select {
	case <-p.leaderExited:
	case <-ctx.Done():
		return ctx.Err()
	}
	p.groupMu.Lock()
	released, err := p.groupReleased, p.leaderErr
	p.groupMu.Unlock()
	if err != nil {
		return err
	}
	if released {
		return nil
	}
	for {
		running, err := p.groupRunning(pid)
		if err != nil {
			return p.failGroupCleanup(pid, err)
		}
		if !running {
			p.groupMu.Lock()
			p.groupReleased = true
			close(p.releaseLeader)
			p.groupMu.Unlock()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (p *Process) failGroupCleanup(pid int, inspectErr error) error {
	p.groupMu.Lock()
	defer p.groupMu.Unlock()
	// Inspection cannot prove completion. Make one last stop attempt while
	// the leader still pins the group, then report failure to both callers.
	killErr := p.forceStop(pid)
	if processGroupMissing(killErr) {
		killErr = nil
	}
	p.groupCleanupErr = errors.Join(
		fmt.Errorf("inspect process group %d: %w", pid, inspectErr),
		wrapSignalError("force stop", p.spec.Path, killErr),
	)
	for _, reader := range p.outputReaders {
		_ = reader.Close()
	}
	p.groupReleased = true
	close(p.releaseLeader)
	return p.groupCleanupErr
}

func wrapSignalError(action, path string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s command %q process group: %w", action, path, err)
}

func wrapStopFailure(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrStopFailed, err)
}

// Run starts the command and waits for it. If ctx is canceled after launch,
// Spec.StopGracePeriod controls when Stop escalates from SIGTERM to SIGKILL.
func (p *Process) Run(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("run command %q: context must not be nil", p.spec.Path)
	}
	if err := p.Start(ctx); err != nil {
		return err
	}

	select {
	case <-p.wait.done:
		return p.waitResult()
	case <-ctx.Done():
		select {
		case <-p.wait.done:
			return p.waitResult()
		default:
		}

		stopCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			p.spec.StopGracePeriod,
		)
		stopErr := wrapStopFailure(p.Stop(stopCtx))
		cancel()
		runErr := fmt.Errorf("run command %q: %w", p.spec.Path, ctx.Err())
		if stopErr != nil {
			return errors.Join(
				runErr,
				stopErr,
				p.outputError(),
			)
		}
		waitErr := p.processResult()
		if errors.Is(waitErr, ErrStopped) {
			waitErr = nil
		}
		return errors.Join(
			runErr,
			waitErr,
			p.outputError(),
		)
	}
}

// Stdout returns a copy of the retained standard output.
func (p *Process) Stdout() []byte {
	return p.output.Bytes()
}

// Stderr returns a copy of the newest 64 KiB written to standard error.
func (p *Process) Stderr() []byte {
	return p.stderr.Bytes()
}
