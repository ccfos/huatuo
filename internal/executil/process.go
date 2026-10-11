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
	"syscall"
	"time"
)

var (
	// ErrStopped reports that Stop terminated the command with a signal.
	ErrStopped = errors.New("executil: command stopped")
	// ErrStopFailed reports that process group termination failed.
	ErrStopFailed = errors.New("executil: stop failed")
	// ErrOutputLimitExceeded reports that memfd data exceeds its read limit.
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
	processStateFinished
	processStateClosedBeforeStart
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
// value return an initialization error, except Stdout and Stderr (no data),
// and Done (nil).
// Process must not be copied.
type Process struct {
	spec       Spec
	stdout     io.Writer
	stderr     io.Writer
	extraFiles []*os.File
	memfd      *memfdOutput

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
func New(spec Spec, options ...Option) (*Process, error) { //nolint:gocritic // Preserve value semantics for command configuration.
	process := &Process{spec: spec}
	for _, option := range options {
		option(process)
	}

	if err := process.spec.validate(); err != nil {
		return nil, fmt.Errorf("new command: %w", err)
	}

	if process.memfd != nil {
		if err := process.memfd.validate(); err != nil {
			return nil, fmt.Errorf("new command: %w", err)
		}
	}

	if process.spec.StopGracePeriod == 0 {
		process.spec.StopGracePeriod = defaultStopGracePeriod
	}

	if process.spec.MaxStdoutBytes == 0 {
		process.spec.MaxStdoutBytes = defaultMaxOutputBytes
	}
	if process.spec.MaxStderrBytes == 0 {
		process.spec.MaxStderrBytes = defaultMaxOutputBytes
	}

	process.spec.Args = slices.Clone(spec.Args)
	process.spec.Env = slices.Clone(spec.Env)
	if process.stdout == nil {
		process.stdout = &outputBuffer{maxBytes: process.spec.MaxStdoutBytes}
	}
	if process.stderr == nil {
		process.stderr = &outputBuffer{maxBytes: process.spec.MaxStderrBytes}
	}
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
		if p.state == processStateClosedBeforeStart {
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

	cmd, err := prepareCommand(p)
	if err != nil {
		return p.failStart(&RunError{ExecutionErr: fmt.Errorf("prepare command %q: %w", p.spec.Path, err)})
	}

	if err := ctx.Err(); err != nil {
		return p.failStart(&RunError{ContextErr: fmt.Errorf("start command %q: %w", p.spec.Path, err)})
	}

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

// prepareCommand runs during Start so New does not acquire output files.
func prepareCommand(process *Process) (*exec.Cmd, error) {
	cmd := exec.Command(process.spec.Path, process.spec.Args...)
	// Descendants outside our group may retain a pipe after the leader exits.
	cmd.WaitDelay = outputDrainTimeout
	cmd.Env = process.spec.Env
	cmd.ExtraFiles = process.extraFiles
	cmd.Stdout = process.stdout
	cmd.Stderr = process.stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		// Give Stop and Close a dedicated process group to signal.
		Setpgid: true,
		// Avoid orphaning the command if its creating OS thread exits.
		Pdeathsig: syscall.SIGKILL,
	}

	if process.memfd != nil {
		if err := process.memfd.prepare(cmd); err != nil {
			return nil, err
		}
	}

	return cmd, nil
}

func (p *Process) finishCanceledStart(launchErr error) error {
	forceErr := wrapStopFailure(p.forceStopAndWait())

	p.mu.Lock()
	var waitErr error
	select {
	case <-p.wait.done:
		p.state = processStateFinished
		if !errors.Is(p.wait.err, ErrStopped) {
			waitErr = p.wait.err
		}
	default:
		p.state = processStateRunning
	}

	failure := &RunError{
		ContextErr:   fmt.Errorf("start command %q: %w", p.spec.Path, launchErr),
		ExecutionErr: waitErr,
		CleanupErr:   forceErr,
	}
	p.start.err = failure

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

	p.state = processStateFinished
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
		p.state = processStateFinished
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

// Wait waits for both the launch result and command reaping. Multiple
// callers receive the same stored result. Inherited output pipes have one second
// to close after the leader is reaped; an incomplete drain follows os/exec's
// ErrWaitDelay semantics. Wait preserves memfd output; retrieve read limit
// errors from MemfdOutput.
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

	<-p.done
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
