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
)

// Stop sends SIGTERM to the process group. The leader's exit, ctx expiration or
// Spec.StopGracePeriod ends the grace period and escalates to SIGKILL.
// Reaping and bounded output draining remain internal to Process.
// Stop preserves output, including memfd. Call Close after reading it.
// Failed process group termination can be retried. Stop rejects an unfinished
// Start; Close instead waits for that Start before forcing cleanup.
func (p *Process) Stop(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, p.spec.StopGracePeriod)
	defer cancel()

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

	if p.state == processStateFinished || p.state == processStateClosedBeforeStart {
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
	p.mu.Unlock()

	err := wrapStopFailure(p.stopProcessGroup(ctx))

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
		return wrapStopFailure(p.forceStopAndWait())
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
		p.state = processStateClosedBeforeStart
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

	stopErr := wrapStopFailure(p.forceStopAndWait())
	return errors.Join(stopErr, p.closeMemfd())
}

func (p *Process) stopProcessGroup(ctx context.Context) error {
	gracefulErr := p.signalProcessGroup(gracefulStopProcessGroup)
	if processGroupMissing(gracefulErr) {
		<-p.wait.done
		return p.stopResult()
	}

	if gracefulErr != nil {
		forceErr := p.forceStopAndWait()
		if forceErr != nil {
			return errors.Join(
				wrapSignalError("gracefully stop", p.spec.Path, gracefulErr),
				forceErr,
			)
		}

		return nil
	}

	select {
	case <-p.wait.done:
		return p.stopResult()
	case <-ctx.Done():
		return p.forceStopAndWait()
	}
}

func (p *Process) forceStopAndWait() error {
	err := p.signalProcessGroup(forceStopProcessGroup)
	if processGroupMissing(err) {
		err = nil
	}

	if err != nil {
		return wrapSignalError("force stop", p.spec.Path, err)
	}

	<-p.wait.done
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
