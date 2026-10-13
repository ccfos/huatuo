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

const outputDrainTimeout = time.Second

type processState uint8

const (
	processRunStateInvalid processState = iota
	processRunStateNew
	processRunStateStarting
	processRunStateRunning
	processRunStateFinished
	processRunStateClosedBeforeStart
)

// Process owns one command lifecycle and its process group. On natural exit,
// remaining group members are killed before the leader is reaped. Stop allows
// group members to finish within its grace period before escalating.
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

	mu          sync.Mutex
	state       processState
	pid         int
	startDone   chan struct{}
	waitDone    chan struct{}
	done        chan struct{}
	terminalErr error

	// Only Start sets this marker; errors from writers cannot cancel launch.
	startCancelErr  error
	isStopRequested bool
	groupStopDone   chan struct{}
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

	if process.stdout == nil {
		process.stdout = &outputBuffer{maxBytes: process.spec.MaxStdoutBytes}
	}
	if process.stderr == nil {
		process.stderr = &outputBuffer{maxBytes: process.spec.MaxStderrBytes}
	}

	process.spec.Args = slices.Clone(spec.Args)
	process.spec.Env = slices.Clone(spec.Env)
	process.state = processRunStateNew
	process.startDone = make(chan struct{})
	process.waitDone = make(chan struct{})
	process.done = make(chan struct{})

	return process, nil
}

// Start launches the command and starts its single internal reaper.
// The context controls launch only; canceling it after Start returns does not
// stop the process. WithMemfdOutput creates a file owned by Process; read it
// with MemfdOutput before calling Close. Failed starts close the created file.
func (p *Process) Start(ctx context.Context) (err error) {
	p.mu.Lock()
	if p.state == processRunStateInvalid {
		p.mu.Unlock()
		return ErrProcessNotInitialized
	}

	if p.state != processRunStateNew {
		if p.state == processRunStateClosedBeforeStart {
			p.mu.Unlock()
			return ErrProcessClosed
		}

		p.mu.Unlock()
		return ErrProcessAlreadyStarted
	}

	p.state = processRunStateStarting
	p.mu.Unlock()

	started := false
	var launchErr error
	defer func() {
		if err == nil {
			return
		}

		if started {
			if stopErr := p.forceStopAndWait(); stopErr != nil {
				p.mu.Lock()
				mayAppendCleanupError(p, stopErr)
				p.mu.Unlock()
			}
		} else {
			p.mu.Lock()
			p.startCancelErr = launchErr
			p.terminalErr = err
			close(p.waitDone)
			p.mu.Unlock()
		}

		err = exitWithCleanup(p)
	}()

	if launchErr = ctx.Err(); launchErr != nil {
		return launchErr
	}

	cmd, err := prepareCommand(p)
	if err != nil {
		return err
	}

	// Argument preparation can cancel the launch context.
	if launchErr = ctx.Err(); launchErr != nil {
		return launchErr
	}

	if err := cmd.Start(); err != nil {
		return err
	}
	started = true

	p.mu.Lock()
	p.pid = cmd.Process.Pid
	launchErr = ctx.Err()
	if launchErr != nil {
		p.isStopRequested = true
		p.startCancelErr = launchErr
		p.terminalErr = launchErr
	}

	go p.reap(cmd)

	if launchErr == nil {
		p.state = processRunStateRunning
		close(p.startDone)
	}

	p.mu.Unlock()

	return launchErr
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

// exitWithCleanup publishes a failed launch after resource release. The caller
// or reaper owns waitDone, so a failed stop can leave reaping in progress.
func exitWithCleanup(p *Process) error {
	cleanupErr := releaseResources(p)

	p.mu.Lock()
	defer p.mu.Unlock()

	mayAppendCleanupError(p, cleanupErr)
	select {
	case <-p.waitDone:
		p.state = processRunStateFinished
	default:
		p.state = processRunStateRunning
	}

	close(p.startDone)
	p.publishDone()
	return p.terminalErr
}

func (p *Process) reap(cmd *exec.Cmd) {
	observeErr := waitForCommandExit(cmd.Process.Pid)

	p.mu.Lock()
	if stopDone := p.groupStopDone; observeErr == nil && stopDone != nil {
		// Keep the leader waitable until Stop has observed or signaled its
		// remaining group members, so the group ID cannot be reused.
		p.mu.Unlock()
		<-stopDone
		p.mu.Lock()
	}
	var groupErr error
	if observeErr != nil {
		groupErr = fmt.Errorf("%w: observe command %q exit: %w", ErrStopFailed, p.spec.Path, observeErr)
	} else if err := syscall.Kill(-p.pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		groupErr = fmt.Errorf("%w: clean up command %q process group: %w", ErrStopFailed, p.spec.Path, err)
	}
	// WNOWAIT pins the leader PID through the final group signal. Retire it
	// before Wait can release it for reuse; later Stop calls cannot signal a reused PID.
	p.pid = 0
	p.mu.Unlock()

	waitErr := cmd.Wait()

	p.mu.Lock()
	defer p.mu.Unlock()

	cleanupErr := groupErr
	switch {
	case groupErr == nil && p.isStopRequested && exitedOnStopSignal(waitErr):
		// Launch cancellation already explains the requested signal exit.
		waitErr = nil
		if p.startCancelErr == nil {
			waitErr = ErrStopped
		}
	case errors.Is(waitErr, exec.ErrWaitDelay):
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("drain command %q output: %w", p.spec.Path, waitErr))
		waitErr = nil
	case waitErr != nil:
		waitErr = fmt.Errorf("wait for command %q: %w", p.spec.Path, waitErr)
	}
	if p.terminalErr == nil {
		p.terminalErr = waitErr
	} else if waitErr != nil {
		p.terminalErr = errors.Join(p.terminalErr, waitErr)
	}
	mayAppendCleanupError(p, cleanupErr)

	// Start owns the Starting transition until its cleanup has finished.
	if p.state == processRunStateRunning {
		p.state = processRunStateFinished
	}

	close(p.waitDone)
	p.publishDone()
}

// mayAppendCleanupError runs under p.mu and marks only cleanup failures.
func mayAppendCleanupError(p *Process, err error) {
	if err == nil {
		return
	}

	if !errors.Is(err, ErrCleanupFailed) {
		err = fmt.Errorf("%w: %w", ErrCleanupFailed, err)
	}

	if p.terminalErr == nil {
		p.terminalErr = err
		return
	}
	p.terminalErr = errors.Join(p.terminalErr, err)
}

// publishDone runs under mu. A canceled launch may finish reaping before Start
// publishes its result, so neither internal notification alone is sufficient.
func (p *Process) publishDone() {
	select {
	case <-p.startDone:
	default:
		return
	}
	select {
	case <-p.waitDone:
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
	if p.state == processRunStateInvalid {
		p.mu.Unlock()
		return ErrProcessNotInitialized
	}

	if p.state == processRunStateNew {
		p.mu.Unlock()
		return ErrProcessNotStarted
	}

	p.mu.Unlock()

	<-p.done
	return p.terminalErr
}
