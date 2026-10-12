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
	"syscall"
)

// Stop sends SIGTERM to the process group, escalating to SIGKILL when the
// context or StopGracePeriod expires. It returns only stopping failures; use
// Wait for the lifecycle result. Signal failures return promptly and can be retried.
// Concurrent callers stop independently. Stop preserves output and rejects
// an unfinished Start; Close instead waits for launch to settle.
func (p *Process) Stop(ctx context.Context) error {
	p.mu.Lock()
	if p.state == processRunStateInvalid {
		p.mu.Unlock()
		return ErrProcessNotInitialized
	}
	if p.state == processRunStateNew || p.state == processRunStateStarting {
		p.mu.Unlock()
		return ErrProcessNotStarted
	}
	p.isStopRequested = true
	p.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, p.spec.StopGracePeriod)
	defer cancel()
	if err := p.stopProcessGroup(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrCleanupFailed, err)
	}

	return nil
}

// Close forcefully stops and reaps a command, then releases owned files. It
// returns only failures of these operations; use Wait for the lifecycle result,
// including reaper cleanup failures. Close waits for an unfinished Start.
// Closing before Start succeeds and publishes ErrProcessClosed to Wait.
// Concurrent and repeated calls are safe. Signal failures can be retried;
// file close errors are cached without changing Wait's immutable result.
// Cleanup errors already published by a failed Start are not returned again.
// Borrowed writers and ExtraFiles remain caller-owned.
func (p *Process) Close() error {
	p.mu.Lock()
	if p.state == processRunStateInvalid {
		p.mu.Unlock()
		return ErrProcessNotInitialized
	}
	if p.state == processRunStateStarting {
		p.mu.Unlock()
		<-p.startDone
		p.mu.Lock()
	}

	if p.state == processRunStateNew {
		p.state = processRunStateClosedBeforeStart
		p.terminalErr = ErrProcessClosed
		close(p.startDone)
		close(p.waitDone)
		p.publishDone()
	}
	p.isStopRequested = true
	p.mu.Unlock()

	stopErr := p.forceStopAndWait()
	if stopErr != nil {
		stopErr = fmt.Errorf("%w: %w", ErrCleanupFailed, stopErr)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	fileErr := p.closeMemfdLocked()
	// A failed launch may have already reported this cached file close failure.
	if fileErr != nil && errors.Is(p.terminalErr, fileErr) {
		fileErr = nil
	}

	return errors.Join(stopErr, fileErr)
}

func (p *Process) stopProcessGroup(ctx context.Context) error {
	gracefulErr := p.signalProcessGroup(syscall.SIGTERM)
	if gracefulErr != nil {
		forceErr := p.forceStopAndWait()
		if forceErr != nil {
			return errors.Join(
				fmt.Errorf("gracefully stop command %q process group: %w", p.spec.Path, gracefulErr),
				forceErr,
			)
		}

		return nil
	}

	select {
	case <-p.waitDone:
		return nil
	case <-ctx.Done():
		return p.forceStopAndWait()
	}
}

func (p *Process) forceStopAndWait() error {
	if err := p.signalProcessGroup(syscall.SIGKILL); err != nil {
		return fmt.Errorf("%w: force stop command %q process group: %w", ErrStopFailed, p.spec.Path, err)
	}

	<-p.waitDone
	return nil
}
