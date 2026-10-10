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
	"io"
	"os"
	"os/exec"
	"slices"
	"sync"
	"time"
)

var (
	// ErrStopped reports that Stop terminated the command with a signal.
	ErrStopped = errors.New("executil: command stopped")
	// ErrStopFailed reports that process group termination failed.
	ErrStopFailed = errors.New("executil: stop failed")
	// ErrOutputLimitExceeded reports that stdout or memfd data exceeds its limit.
	ErrOutputLimitExceeded = errors.New("executil: output limit exceeded")

	errProcessNotInitialized = errors.New("process is not initialized")
)

const outputDrainTimeout = time.Second

type processState uint8

const (
	processStateInvalid processState = iota
	processStateNew
	processStateStarting
	processStateRunning
	processStateExited
	processStateStartFailed
	processStateClosed
)

// Closing done publishes the immutable error to every waiter.
type lifecycleResult struct {
	done chan struct{}
	err  error
}

// Process owns one command lifecycle and its process group. The group leader's
// exit ends the command lifetime; remaining group members are killed before the
// leader is reaped. Children must finish their work before the leader exits.
// Process must be created with New and cannot be restarted; methods on its zero
// value return an initialization error, except Stderr (no data) and Done (nil).
// Process must not be copied.
type Process struct {
	spec         Spec
	output       outputBuffer
	stderr       tailBuffer
	extraFiles   []*os.File
	stdoutWriter io.Writer
	stderrWriter io.Writer
	memfd        *memfdOutput

	mu              sync.Mutex
	state           processState
	pid             int
	start           lifecycleResult
	wait            lifecycleResult
	done            chan struct{}
	groupErr        error
	isStopRequested bool
	stopAttempt     *lifecycleResult
}

// New validates and snapshots a command specification without starting it.
func New(spec Spec, options ...Option) (*Process, error) { //nolint:gocritic // Spec is within the 80-byte value limit.
	process := &Process{spec: spec}
	for _, option := range options {
		option(process)
	}

	if err := process.spec.validate(); err != nil {
		return nil, fmt.Errorf("new command: %w", err)
	}

	if process.memfd != nil {
		if process.memfd.argsForOutput == nil {
			return nil, errors.New("new command: output argument builder must not be nil")
		}

		if process.memfd.limit <= 0 {
			return nil, errors.New("new command: maximum memfd output bytes must be positive")
		}
	}

	if process.spec.StopGracePeriod == 0 {
		process.spec.StopGracePeriod = defaultStopGracePeriod
	}

	if process.spec.MaxOutputBytes == 0 {
		process.spec.MaxOutputBytes = defaultMaxOutputBytes
	}

	process.spec.Args = slices.Clone(process.spec.Args)
	process.spec.Env = slices.Clone(process.spec.Env)
	process.output = outputBuffer{limit: process.spec.MaxOutputBytes}
	process.state = processStateNew
	process.start = lifecycleResult{done: make(chan struct{})}
	process.wait = lifecycleResult{done: make(chan struct{})}
	process.done = make(chan struct{})

	return process, nil
}

// Start launches the command and starts its single internal reaper.
// The context controls launch only; canceling it after Start returns does not
// stop the process. WithMemfdOutput creates a file owned by Process; read it
// with MemfdOutput before calling Close. Failed starts close the created file.
func (p *Process) Start(ctx context.Context) error {
	p.mu.Lock()
	if p.state == processStateInvalid {
		p.mu.Unlock()
		return fmt.Errorf("start command: %w", errProcessNotInitialized)
	}

	if p.state != processStateNew {
		if p.state == processStateClosed {
			p.mu.Unlock()
			return fmt.Errorf("start command %q: %w", p.spec.Path, os.ErrClosed)
		}

		p.mu.Unlock()
		return fmt.Errorf("start command %q: process has already been started", p.spec.Path)
	}

	p.state = processStateStarting
	p.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return p.failStart(&RunError{ContextErr: fmt.Errorf("start command %q: %w", p.spec.Path, err)})
	}

	cmd := exec.Command(p.spec.Path, p.spec.Args...)
	// Descendants outside our group may retain a pipe after the leader exits.
	cmd.WaitDelay = outputDrainTimeout
	cmd.Env = p.spec.Env
	cmd.ExtraFiles = p.extraFiles
	cmd.Stdout = &p.output
	if p.stdoutWriter != nil {
		cmd.Stdout = p.stdoutWriter
	}

	cmd.Stderr = &p.stderr
	if p.stderrWriter != nil {
		cmd.Stderr = p.stderrWriter
	}

	if p.memfd != nil {
		if err := p.memfd.prepare(cmd); err != nil {
			return p.failStart(&RunError{ExecutionErr: fmt.Errorf("start command %q: %w", p.spec.Path, err)})
		}
	}

	if err := ctx.Err(); err != nil {
		return p.failStart(&RunError{ContextErr: fmt.Errorf("start command %q: %w", p.spec.Path, err)})
	}

	configureCommand(cmd)
	if err := cmd.Start(); err != nil {
		return p.failStart(&RunError{ExecutionErr: fmt.Errorf("start command %q: %w", p.spec.Path, err)})
	}

	p.mu.Lock()
	p.pid = cmd.Process.Pid
	launchErr := ctx.Err()
	if launchErr != nil {
		p.isStopRequested = true
	}

	go p.reap(cmd)
	if launchErr == nil {
		p.state = processStateRunning
		close(p.start.done)
	}

	p.mu.Unlock()

	if launchErr == nil {
		return nil
	}

	return p.finishCanceledStart(launchErr)
}

func (p *Process) finishCanceledStart(launchErr error) error {
	forceErr := wrapStopFailure(p.forceStopAndWait(p.wait.done))

	p.mu.Lock()
	var waitErr error
	select {
	case <-p.wait.done:
		if !errors.Is(p.wait.err, ErrStopped) {
			waitErr = p.wait.err
		}
	default:
	}

	failure := &RunError{
		ContextErr:   fmt.Errorf("start command %q: %w", p.spec.Path, launchErr),
		ExecutionErr: waitErr,
		CleanupErr:   forceErr,
	}
	p.start.err = failure
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

	if p.memfd != nil {
		failure.CleanupErr = errors.Join(failure.CleanupErr, p.memfd.close())
	}

	close(p.start.done)
	p.publishDone()
	startErr := p.start.err
	p.mu.Unlock()
	return startErr
}

func (p *Process) failStart(err *RunError) error {
	p.mu.Lock()
	if p.memfd != nil {
		err.CleanupErr = errors.Join(err.CleanupErr, p.memfd.close())
	}

	p.state = processStateStartFailed
	p.start.err = err
	p.wait.err = err
	close(p.start.done)
	close(p.wait.done)
	p.publishDone()
	p.mu.Unlock()
	return err
}

func (p *Process) reap(cmd *exec.Cmd) {
	exitErr := waitForCommandExit(cmd.Process.Pid)
	p.mu.Lock()
	if exitErr != nil {
		p.groupErr = wrapStopFailure(fmt.Errorf("observe command %q exit: %w", p.spec.Path, exitErr))
	} else if err := forceStopProcessGroup(p.pid); err != nil && !processGroupMissing(err) {
		p.groupErr = wrapStopFailure(wrapSignalError("clean up", p.spec.Path, err))
	}
	// WNOWAIT pins the leader PID through the final group signal. Retire it
	// before Wait can release it for reuse; later Stop calls only read results.
	p.pid = 0
	p.mu.Unlock()

	err := cmd.Wait()

	p.mu.Lock()
	isExpectedStop := p.groupErr == nil && p.isStopRequested
	if isExpectedStop && isStoppedExit(err) {
		err = fmt.Errorf(
			"%w: command %q exited after a stop signal: %w",
			ErrStopped,
			p.spec.Path,
			err,
		)
	} else if err != nil {
		err = fmt.Errorf("wait for command %q: %w", p.spec.Path, err)
	}

	p.wait.err = err
	if p.state == processStateRunning {
		p.state = processStateExited
	}

	close(p.wait.done)
	p.publishDone()
	p.mu.Unlock()
}

// publishDone runs under mu. A canceled launch may finish reaping before Start
// publishes its result, so neither internal notification alone is sufficient.
func (p *Process) publishDone() {
	select {
	case <-p.start.done:
	default:
		return
	}
	select {
	case <-p.wait.done:
	default:
		return
	}
	close(p.done)
}

// Done closes when Wait's final result is available, including failed starts and
// Close before Start. Before Start it remains open. The zero value returns nil.
func (p *Process) Done() <-chan struct{} {
	return p.done
}

// Wait waits for an in-progress Start and then for the command reaper. Multiple
// callers receive the same stored result. Inherited output pipes have one second
// to close after the leader is reaped; an incomplete drain follows os/exec's
// ErrWaitDelay semantics. Wait preserves memfd output and does not report
// output size limits; retrieve those errors from Stdout and MemfdOutput.
func (p *Process) Wait() error {
	p.mu.Lock()
	if p.state == processStateInvalid {
		p.mu.Unlock()
		return fmt.Errorf("wait for command: %w", errProcessNotInitialized)
	}

	if p.state == processStateNew {
		p.mu.Unlock()
		return fmt.Errorf("wait for command %q: process has not been started", p.spec.Path)
	}

	p.mu.Unlock()

	// Failed starts also publish both results once no process remains to reap.
	<-p.start.done
	<-p.wait.done
	return p.processResult()
}

func (p *Process) processResult() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	err := p.wait.err
	if p.start.err != nil {
		err = p.start.err
		// A canceled launch can fail to signal the child and return before
		// reaping. Preserve any independent exit failure observed later.
		if p.wait.err != nil && !errors.Is(p.wait.err, ErrStopped) && !errors.Is(err, p.wait.err) {
			err = errors.Join(err, p.wait.err)
		}
	}

	if p.groupErr == nil || errors.Is(err, p.groupErr) {
		return err
	}

	return errors.Join(err, p.groupErr)
}

// Stop sends SIGTERM to the process group. The leader's exit, ctx expiration or
// Spec.StopGracePeriod ends the grace period and escalates to SIGKILL.
// Reaping and bounded output draining remain internal to Process.
// Stop preserves output, including memfd. Call Close after reading it.
// Failed process group termination can be retried. Stop rejects an unfinished
// Start; Close instead waits for that Start before forcing cleanup.
func (p *Process) Stop(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, p.spec.StopGracePeriod)
	defer cancel()
	return p.stop(ctx)
}

func (p *Process) stop(ctx context.Context) error {
	p.mu.Lock()
	if p.state == processStateInvalid {
		p.mu.Unlock()
		return fmt.Errorf("stop command: %w", errProcessNotInitialized)
	}

	switch p.state {
	case processStateNew, processStateStarting:
		p.mu.Unlock()
		return fmt.Errorf("stop command %q: process has not been started", p.spec.Path)
	}

	if p.state == processStateStartFailed || p.state == processStateExited || p.state == processStateClosed {
		p.mu.Unlock()
		return p.stopResult()
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

	p.isStopRequested = true
	attempt := &lifecycleResult{done: make(chan struct{})}
	p.stopAttempt = attempt
	waitDone := p.wait.done
	p.mu.Unlock()

	err := wrapStopFailure(p.stopProcessGroup(ctx, waitDone))

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
		return wrapStopFailure(p.forceStopAndWait(p.wait.done))
	}
}

// Close forcefully stops and reaps a running command, then releases owned files.
// It waits for an in-progress Start. Calling Close before Start permanently
// closes the process; Wait then returns os.ErrClosed. Concurrent and repeated
// calls are safe. Signal failures can be retried; file close failures are cached.
// Borrowed writers and ExtraFiles are never closed. Close does not report an
// ordinary exit failure: use Wait to obtain the execution result.
func (p *Process) Close() error {
	p.mu.Lock()
	if p.state == processStateInvalid {
		p.mu.Unlock()
		return fmt.Errorf("close command: %w", errProcessNotInitialized)
	}
	if p.state == processStateNew {
		p.state = processStateClosed
		p.start.err = fmt.Errorf("command %q closed before start: %w", p.spec.Path, os.ErrClosed)
		p.wait.err = p.start.err
		close(p.start.done)
		close(p.wait.done)
		p.publishDone()
	}
	p.mu.Unlock()

	<-p.start.done
	p.mu.Lock()
	p.isStopRequested = true
	p.mu.Unlock()

	stopErr := wrapStopFailure(p.forceStopAndWait(p.wait.done))
	return errors.Join(stopErr, p.closeMemfd())
}

func (p *Process) stopProcessGroup(ctx context.Context, waitDone <-chan struct{}) error {
	gracefulErr := p.signalProcessGroup(gracefulStopProcessGroup)
	if processGroupMissing(gracefulErr) {
		<-waitDone
		return p.stopResult()
	}

	if gracefulErr != nil {
		forceErr := p.forceStopAndWait(waitDone)
		if forceErr != nil {
			return errors.Join(
				wrapSignalError("gracefully stop", p.spec.Path, gracefulErr),
				forceErr,
			)
		}

		return nil
	}

	select {
	case <-waitDone:
		return p.stopResult()
	case <-ctx.Done():
		return p.forceStopAndWait(waitDone)
	}
}

func (p *Process) forceStopAndWait(waitDone <-chan struct{}) error {
	err := p.signalProcessGroup(forceStopProcessGroup)
	if processGroupMissing(err) {
		err = nil
	}

	if err != nil {
		return wrapSignalError("force stop", p.spec.Path, err)
	}

	<-waitDone
	return p.stopResult()
}

func (p *Process) signalProcessGroup(signal func(int) error) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.pid == 0 {
		return nil
	}

	return signal(p.pid)
}

func (p *Process) stopResult() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if errors.Is(p.wait.err, exec.ErrWaitDelay) {
		return errors.Join(p.groupErr, p.wait.err)
	}

	return p.groupErr
}

func wrapSignalError(action, path string, err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf(
		"%s command %q process group: %w",
		action,
		path,
		err,
	)
}

func wrapStopFailure(err error) error {
	if err == nil || errors.Is(err, ErrStopFailed) {
		return err
	}

	if errors.Is(err, exec.ErrWaitDelay) {
		return err
	}

	return fmt.Errorf("%w: %w", ErrStopFailed, err)
}

// Run starts the command and waits for it. If ctx is canceled after launch,
// Spec.StopGracePeriod controls when Stop escalates from SIGTERM to SIGKILL.
// Run preserves memfd output, including after cancellation. Read outputs and
// call Close to release the file. Output size errors come from the output methods.
func (p *Process) Run(ctx context.Context) error {
	if err := p.Start(ctx); err != nil {
		return err
	}

	select {
	case <-p.wait.done:
		return p.runResult(nil, nil)
	case <-ctx.Done():
		select {
		case <-p.wait.done:
			return p.runResult(nil, nil)
		default:
		}

		stopErr := p.Stop(context.WithoutCancel(ctx))
		return p.runResult(fmt.Errorf("run command %q: %w", p.spec.Path, ctx.Err()), stopErr)
	}
}

func (p *Process) runResult(contextErr, stopErr error) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	executionErr := p.wait.err
	if contextErr != nil && errors.Is(executionErr, ErrStopped) {
		executionErr = nil
	}
	cleanupErr := errors.Join(stopErr, p.groupErr)
	if contextErr == nil && executionErr == nil && cleanupErr == nil {
		return nil
	}

	return &RunError{
		ContextErr:   contextErr,
		ExecutionErr: executionErr,
		CleanupErr:   cleanupErr,
	}
}
