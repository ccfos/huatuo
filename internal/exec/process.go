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
	osexec "os/exec"
	"slices"
	"strings"
	"sync"
	"time"
)

// ErrStopped reports that Stop terminated the command with a signal.
var ErrStopped = errors.New("exec: command stopped")

var errProcessNotInitialized = errors.New("process is not initialized")

// Spec describes one external command invocation.
type Spec struct {
	Path string
	Args []string
	// Env replaces the child environment. A nil Env inherits the parent environment.
	Env []string
	// Stdout receives standard output. A nil Stdout retains it as diagnostic output.
	Stdout io.Writer
}

type processState uint8

const (
	processStateNew processState = iota
	processStateStarting
	processStateRunning
	processStateExited
	processStateStartFailed
)

// Process owns one command lifecycle. It must be created with New and cannot be
// restarted; methods on its zero value return an initialization error.
type Process struct {
	spec   Spec
	output tailBuffer

	mu              sync.Mutex
	state           processState
	cmd             *osexec.Cmd
	startDone       chan struct{}
	startErr        error
	waitDone        chan struct{}
	waitErr         error
	isReaped        bool
	isStopRequested bool
	isStopStarted   bool
	stopDone        chan struct{}
	stopErr         error
	startCommand    func(*osexec.Cmd) error
	forceStop       func(int) error
}

// New validates and snapshots a command specification without starting it.
func New(spec Spec) (*Process, error) { //nolint:gocritic // Spec is at the project's 80-byte value limit.
	if err := validateSpec(&spec); err != nil {
		return nil, fmt.Errorf("new command: %w", err)
	}

	spec.Args = slices.Clone(spec.Args)
	spec.Env = slices.Clone(spec.Env)
	return &Process{
		spec:      spec,
		startDone: make(chan struct{}),
		waitDone:  make(chan struct{}),
		stopDone:  make(chan struct{}),
		startCommand: func(cmd *osexec.Cmd) error {
			return cmd.Start()
		},
		forceStop: forceStopProcessGroup,
	}, nil
}

func validateSpec(spec *Spec) error {
	if strings.TrimSpace(spec.Path) == "" {
		return errors.New("command path must not be empty")
	}
	if strings.IndexByte(spec.Path, 0) >= 0 {
		return fmt.Errorf("command path %q contains a null byte", spec.Path)
	}
	for index, arg := range spec.Args {
		if strings.IndexByte(arg, 0) >= 0 {
			return fmt.Errorf("command argument %d contains a null byte", index)
		}
	}
	for index, value := range spec.Env {
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
	if !p.isInitialized() {
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
	cmd.Env = slices.Clone(p.spec.Env)
	if p.spec.Stdout == nil {
		cmd.Stdout = &p.output
	} else {
		cmd.Stdout = p.spec.Stdout
	}
	cmd.Stderr = &p.output

	if err := configureCommand(cmd); err != nil {
		return p.failStart(fmt.Errorf("configure command %q: %w", p.spec.Path, err))
	}
	if err := p.startCommand(cmd); err != nil {
		return p.failStart(fmt.Errorf("start command %q: %w", p.spec.Path, err))
	}

	p.mu.Lock()
	p.cmd = cmd
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

	killErr := p.forceStop(cmd.Process.Pid)
	if processGroupMissing(killErr) {
		killErr = nil
	}
	if killErr == nil {
		<-p.waitDone
	}

	p.mu.Lock()
	var waitErr error
	if killErr == nil && !errors.Is(p.waitErr, ErrStopped) {
		waitErr = p.waitErr
	}
	p.startErr = errors.Join(
		fmt.Errorf("start command %q: %w", p.spec.Path, launchErr),
		wrapSignalError("force stop", p.spec.Path, killErr),
		waitErr,
	)
	if killErr == nil {
		p.state = processStateStartFailed
	} else if p.isReaped {
		p.state = processStateExited
	} else {
		p.state = processStateRunning
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
	p.isReaped = true
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
		if !p.isInitialized() {
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
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.startErr != nil {
		return p.startErr
	}
	return p.waitErr
}

// Stop sends SIGTERM to the process group and waits until ctx expires before
// escalating to SIGKILL. Process reaping remains internal to Process.
func (p *Process) Stop(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("stop command %q: context must not be nil", p.spec.Path)
	}

	p.mu.Lock()
	if !p.isInitialized() {
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
	if p.isStopStarted {
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

	p.isStopStarted = true
	p.isStopRequested = true
	pid := p.cmd.Process.Pid
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
		forceErr := p.forceStop(pid)
		if processGroupMissing(forceErr) {
			forceErr = nil
		}
		if forceErr != nil {
			return errors.Join(
				wrapSignalError("gracefully stop", p.spec.Path, gracefulErr),
				wrapSignalError("force stop", p.spec.Path, forceErr),
			)
		}
		<-waitDone
		return nil
	}

	select {
	case <-waitDone:
		return nil
	case <-ctx.Done():
		forceErr := p.forceStop(pid)
		if processGroupMissing(forceErr) {
			forceErr = nil
		}
		if forceErr != nil {
			return wrapSignalError("force stop", p.spec.Path, forceErr)
		}
		<-waitDone
		return nil
	}
}

func wrapSignalError(action, path string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s command %q process group: %w", action, path, err)
}

func (p *Process) isInitialized() bool {
	return p.startDone != nil && p.waitDone != nil && p.stopDone != nil &&
		p.startCommand != nil && p.forceStop != nil
}

// Run starts the command and waits for it. If ctx is canceled after launch,
// stopGracePeriod controls when Stop escalates from SIGTERM to SIGKILL.
func (p *Process) Run(ctx context.Context, stopGracePeriod time.Duration) error {
	if ctx == nil {
		return fmt.Errorf("run command %q: context must not be nil", p.spec.Path)
	}
	if stopGracePeriod <= 0 {
		return fmt.Errorf("run command %q: stop grace period must be greater than zero", p.spec.Path)
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

		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopGracePeriod)
		stopErr := p.Stop(stopCtx)
		cancel()
		waitErr := p.Wait()
		if errors.Is(waitErr, ErrStopped) {
			waitErr = nil
		}
		return errors.Join(
			fmt.Errorf("run command %q: %w", p.spec.Path, ctx.Err()),
			stopErr,
			waitErr,
		)
	}
}

// OutputTail returns the newest retained stderr and, when Spec.Stdout is nil,
// stdout. At most 64 KiB is retained.
func (p *Process) OutputTail() string {
	return p.output.String()
}
