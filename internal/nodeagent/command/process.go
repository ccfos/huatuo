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

// Package command owns operating-system commands started by the Node Agent.
package command

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"sync"
)

// ErrStopped indicates that the command exited because Stop terminated it.
var ErrStopped = errors.New("command: stopped")

var (
	errAlreadyStarted = errors.New("command has already been started")
	errNotStarted     = errors.New("command has not been started")
)

// Spec defines one command invocation.
type Spec struct {
	Path        string
	Args        []string
	Env         []string
	OutputLimit int
}

type processState uint8

const (
	processStateNew processState = iota
	processStateStarting
	processStateRunning
	processStateWaiting
	processStateExited
	processStateStartFailed
)

// Process owns one command, its process group, and bounded diagnostic output.
type Process struct {
	spec   Spec
	output *tailBuffer

	mu            sync.Mutex
	state         processState
	cmd           *exec.Cmd
	startErr      error
	waitDone      chan struct{}
	waitErr       error
	stopRequested bool
	stopStarted   bool
	stopDone      chan struct{}
	stopErr       error
}

// New validates and snapshots a command specification without starting it.
func New(spec Spec) (*Process, error) {
	if err := validateSpec(spec); err != nil {
		return nil, err
	}
	spec.Args = slices.Clone(spec.Args)
	spec.Env = slices.Clone(spec.Env)
	return &Process{
		spec:     spec,
		output:   newTailBuffer(spec.OutputLimit),
		waitDone: make(chan struct{}),
		stopDone: make(chan struct{}),
	}, nil
}

// Start starts the command exactly once.
func (p *Process) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("start command: context is required")
	}

	p.mu.Lock()
	if p.state != processStateNew {
		p.mu.Unlock()
		return fmt.Errorf("start command %q: %w", p.spec.Path, errAlreadyStarted)
	}
	p.state = processStateStarting
	p.mu.Unlock()

	if err := ctx.Err(); err != nil {
		startErr := fmt.Errorf("start command %q: %w", p.spec.Path, err)
		p.finishStartFailure(startErr)
		return startErr
	}

	cmd := exec.Command(p.spec.Path, p.spec.Args...)
	cmd.Env = slices.Clone(p.spec.Env)
	cmd.Stdout = p.output
	cmd.Stderr = p.output
	if err := configureCommand(cmd); err != nil {
		startErr := fmt.Errorf("start command %q: %w", p.spec.Path, err)
		p.finishStartFailure(startErr)
		return startErr
	}
	p.mu.Lock()
	p.cmd = cmd
	p.mu.Unlock()

	if err := cmd.Start(); err != nil {
		startErr := fmt.Errorf("start command %q: %w", p.spec.Path, err)
		p.finishStartFailure(startErr)
		return startErr
	}

	if err := ctx.Err(); err != nil {
		cleanupErr := forceStopProcessGroup(cmd.Process.Pid)
		if processGroupMissing(cleanupErr) {
			cleanupErr = nil
		}
		waitErr := cmd.Wait()
		startErr := fmt.Errorf("start command %q: %w", p.spec.Path, err)
		if cleanupErr != nil {
			startErr = errors.Join(
				startErr,
				fmt.Errorf("clean up command process group %d: %w", cmd.Process.Pid, cleanupErr),
			)
		}
		if waitErr != nil && !isStoppedExit(waitErr) {
			startErr = errors.Join(startErr, fmt.Errorf("wait for command cleanup: %w", waitErr))
		}
		p.finishCanceledStart(startErr)
		return startErr
	}

	p.mu.Lock()
	p.state = processStateRunning
	p.mu.Unlock()
	return nil
}

// Wait waits for the started command and reaps its process exactly once.
func (p *Process) Wait() error {
	p.mu.Lock()
	switch p.state {
	case processStateNew, processStateStarting:
		p.mu.Unlock()
		return fmt.Errorf("wait for command %q: %w", p.spec.Path, errNotStarted)
	case processStateStartFailed:
		err := p.startErr
		p.mu.Unlock()
		return err
	case processStateWaiting, processStateExited:
		done := p.waitDone
		p.mu.Unlock()
		<-done
		return p.waitResult()
	case processStateRunning:
		p.state = processStateWaiting
		cmd := p.cmd
		p.mu.Unlock()

		err := cmd.Wait()
		p.mu.Lock()
		if p.stopRequested && isStoppedExit(err) {
			err = fmt.Errorf("%w: %w", ErrStopped, err)
		} else if err != nil {
			err = fmt.Errorf("wait for command %q: %w", p.spec.Path, err)
		}
		p.waitErr = err
		p.state = processStateExited
		close(p.waitDone)
		p.mu.Unlock()
		return err
	default:
		p.mu.Unlock()
		return errors.New("wait for command: invalid process state")
	}
}

// Stop requests graceful process-group termination and escalates when ctx ends.
func (p *Process) Stop(ctx context.Context) error {
	if ctx == nil {
		return errors.New("stop command: context is required")
	}

	p.mu.Lock()
	switch p.state {
	case processStateNew, processStateStarting:
		p.mu.Unlock()
		return fmt.Errorf("stop command %q: %w", p.spec.Path, errNotStarted)
	case processStateStartFailed, processStateExited:
		p.mu.Unlock()
		return nil
	}
	if p.stopStarted {
		done := p.stopDone
		p.mu.Unlock()
		select {
		case <-done:
			return p.stopResult()
		case <-ctx.Done():
			return fmt.Errorf("wait for command %q stop: %w", p.spec.Path, ctx.Err())
		}
	}
	p.stopStarted = true
	p.stopRequested = true
	pid := p.cmd.Process.Pid
	waitDone := p.waitDone
	p.mu.Unlock()

	stopErr := gracefulStopProcessGroup(pid)
	if processGroupMissing(stopErr) {
		stopErr = nil
	} else if stopErr != nil {
		gracefulErr := fmt.Errorf("gracefully stop command process group %d: %w", pid, stopErr)
		forceErr := forceStopProcessGroup(pid)
		if processGroupMissing(forceErr) {
			stopErr = nil
		} else if forceErr != nil {
			stopErr = errors.Join(
				gracefulErr,
				fmt.Errorf("force stop command process group %d: %w", pid, forceErr),
			)
		} else {
			stopErr = nil
		}
	} else {
		select {
		case <-waitDone:
			stopErr = nil
		default:
			select {
			case <-waitDone:
				stopErr = nil
			case <-ctx.Done():
				stopErr = forceStopProcessGroup(pid)
				if processGroupMissing(stopErr) {
					stopErr = nil
				} else if stopErr != nil {
					stopErr = fmt.Errorf("force stop command process group %d: %w", pid, stopErr)
				}
			}
		}
	}

	p.mu.Lock()
	p.stopErr = stopErr
	close(p.stopDone)
	p.mu.Unlock()
	return stopErr
}

// OutputTail returns a snapshot of the newest combined stdout and stderr.
func (p *Process) OutputTail() string {
	return p.output.String()
}

func (p *Process) finishStartFailure(err error) {
	p.mu.Lock()
	p.startErr = err
	p.state = processStateStartFailed
	close(p.waitDone)
	p.mu.Unlock()
}

func (p *Process) finishCanceledStart(err error) {
	p.mu.Lock()
	p.startErr = err
	p.waitErr = err
	p.state = processStateStartFailed
	close(p.waitDone)
	p.mu.Unlock()
}

func (p *Process) waitResult() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitErr
}

func (p *Process) stopResult() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopErr
}

func validateSpec(spec Spec) error {
	if strings.TrimSpace(spec.Path) == "" {
		return errors.New("new command: path is required")
	}
	if strings.ContainsRune(spec.Path, 0) {
		return errors.New("new command: path contains a null byte")
	}
	if spec.OutputLimit <= 0 {
		return errors.New("new command: output limit must be greater than zero")
	}
	for index, arg := range spec.Args {
		if strings.ContainsRune(arg, 0) {
			return fmt.Errorf("new command: argument %d contains a null byte", index)
		}
	}
	for index, env := range spec.Env {
		if strings.ContainsRune(env, 0) {
			return fmt.Errorf("new command: environment entry %d contains a null byte", index)
		}
		separator := strings.IndexByte(env, '=')
		if separator <= 0 {
			return fmt.Errorf("new command: environment entry %d must use key=value", index)
		}
	}
	return nil
}
