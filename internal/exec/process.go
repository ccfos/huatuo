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
	osexec "os/exec"
	"slices"
	"strings"
	"sync"
	"time"
)

// ErrStopped reports that Stop terminated the command with a signal.
var ErrStopped = errors.New("exec: command stopped")

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

// Process owns one command lifecycle. It must be created with New and cannot be
// restarted; methods on its zero value return an initialization error.
type Process struct {
	spec   Spec
	output outputBuffer
	stderr tailBuffer

	mu              sync.Mutex
	state           processState
	pid             int
	startDone       chan struct{}
	startErr        error
	waitDone        chan struct{}
	waitErr         error
	isStopRequested bool
	stopDone        chan struct{}
	stopErr         error
	startCommand    func(*osexec.Cmd) error
	forceStop       func(int) error
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
		spec:         spec,
		output:       newOutputBuffer(spec.MaxOutputBytes),
		state:        processStateNew,
		startDone:    make(chan struct{}),
		waitDone:     make(chan struct{}),
		startCommand: (*osexec.Cmd).Start,
		forceStop:    forceStopProcessGroup,
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
	cmd.Stdout = &p.output
	cmd.Stderr = &p.stderr

	configureCommand(cmd)
	if err := p.startCommand(cmd); err != nil {
		return p.failStart(fmt.Errorf("start command %q: %w", p.spec.Path, err))
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
		close(p.startDone)
	}
	p.mu.Unlock()

	if launchErr == nil {
		return nil
	}

	forceErr := p.forceStopAndWait(cmd.Process.Pid, p.waitDone)

	p.mu.Lock()
	var waitErr error
	if forceErr == nil && !errors.Is(p.waitErr, ErrStopped) {
		waitErr = p.waitErr
	}
	p.startErr = errors.Join(
		fmt.Errorf("start command %q: %w", p.spec.Path, launchErr),
		forceErr,
		waitErr,
	)
	if forceErr == nil {
		p.state = processStateStartFailed
	} else {
		select {
		case <-p.waitDone:
			p.state = processStateExited
		default:
			p.state = processStateRunning
		}
	}
	close(p.startDone)
	startErr := p.startErr
	p.mu.Unlock()
	return startErr
}

func (p *Process) failStart(err error) error {
	p.mu.Lock()
	p.state = processStateStartFailed
	p.startErr = err
	p.waitErr = err
	close(p.startDone)
	close(p.waitDone)
	p.mu.Unlock()
	return err
}

func (p *Process) reap(cmd *osexec.Cmd) {
	err := cmd.Wait()

	p.mu.Lock()
	if p.isStopRequested && isStoppedExit(err) {
		err = fmt.Errorf("%w: command %q exited after a stop signal: %w", ErrStopped, p.spec.Path, err)
	} else if err != nil {
		err = fmt.Errorf("wait for command %q: %w", p.spec.Path, err)
	}
	p.waitErr = err
	if p.state == processStateRunning {
		p.state = processStateExited
	}
	close(p.waitDone)
	p.mu.Unlock()
}

// Wait waits for an in-progress Start and then for the command reaper. Multiple
// callers receive the same stored result.
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
			done := p.startDone
			p.mu.Unlock()
			<-done
		case processStateStartFailed:
			err := p.startErr
			p.mu.Unlock()
			return err
		case processStateRunning, processStateExited:
			done := p.waitDone
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
	if p.startErr != nil {
		return p.startErr
	}
	return p.waitErr
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
	case processStateStartFailed, processStateExited:
		p.mu.Unlock()
		return nil
	}
	if p.stopDone != nil {
		done := p.stopDone
		p.mu.Unlock()
		select {
		case <-done:
			p.mu.Lock()
			err := p.stopErr
			p.mu.Unlock()
			return err
		case <-ctx.Done():
			return fmt.Errorf("wait for command %q stop: %w", p.spec.Path, ctx.Err())
		}
	}

	p.isStopRequested = true
	p.stopDone = make(chan struct{})
	pid := p.pid
	waitDone := p.waitDone
	p.mu.Unlock()

	err := p.stopProcessGroup(ctx, pid, waitDone)

	p.mu.Lock()
	p.stopErr = err
	close(p.stopDone)
	p.mu.Unlock()
	return err
}

func (p *Process) stopProcessGroup(ctx context.Context, pid int, waitDone <-chan struct{}) error {
	gracefulErr := gracefulStopProcessGroup(pid)
	if processGroupMissing(gracefulErr) {
		<-waitDone
		return nil
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

	select {
	case <-waitDone:
		return nil
	case <-ctx.Done():
		return p.forceStopAndWait(pid, waitDone)
	}
}

func (p *Process) forceStopAndWait(pid int, waitDone <-chan struct{}) error {
	err := p.forceStop(pid)
	if processGroupMissing(err) {
		err = nil
	}
	if err != nil {
		return wrapSignalError("force stop", p.spec.Path, err)
	}
	<-waitDone
	return nil
}

func wrapSignalError(action, path string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s command %q process group: %w", action, path, err)
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
	case <-p.waitDone:
		return p.waitResult()
	case <-ctx.Done():
		select {
		case <-p.waitDone:
			return p.waitResult()
		default:
		}

		stopCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			p.spec.StopGracePeriod,
		)
		stopErr := p.Stop(stopCtx)
		cancel()
		<-p.waitDone
		waitErr := p.processResult()
		if errors.Is(waitErr, ErrStopped) {
			waitErr = nil
		}
		return errors.Join(
			fmt.Errorf("run command %q: %w", p.spec.Path, ctx.Err()),
			stopErr,
			waitErr,
			p.outputError(),
		)
	}
}

// Output returns a copy of the retained standard output.
func (p *Process) Output() []byte {
	return p.output.Bytes()
}

// Err returns a copy of the newest 64 KiB written to standard error.
func (p *Process) Err() []byte {
	return p.stderr.Bytes()
}
