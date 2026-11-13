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
)

// Result owns output snapshots that remain valid after the command is closed.
// Redirected streams are empty. Memfd is empty unless WithMemfdOutput was used.
// Stdout and Stderr retain only their configured tails. Failed commands can
// return partial output; a memfd read limit error makes Memfd incomplete.
type Result struct {
	Stdout []byte
	Stderr []byte
	Memfd  []byte
}

// Run executes, captures output and closes one command. It always returns a
// non-nil Result, which is empty for invalid configuration. Other failures
// return the available output snapshots.
// Pure cancellation and timeout errors match ErrRunCanceled and ErrRunTimeout,
// respectively, after output collection and cleanup finish.
// Cancellation is an error. A failed stop gets a final forceful cleanup attempt;
// earlier stop failures remain reported even if recovery succeeds. Actual
// cleanup failures match ErrCleanupFailed.
func Run(ctx context.Context, spec Spec, options ...Option) (*Result, error) { //nolint:gocritic // Preserve value semantics for command configuration.
	p, err := New(spec, options...)
	if err != nil {
		return &Result{}, err
	}

	if err := p.Start(ctx); err != nil {
		return finishRun(p, nil, nil)
	}

	select {
	case <-p.Done():
		return finishRun(p, nil, nil)
	case <-ctx.Done():
	}

	// Completion wins over a cancellation observed after the final result.
	select {
	case <-p.Done():
		return finishRun(p, nil, nil)
	default:
	}

	contextErr := fmt.Errorf("run command %q: %w", spec.Path, ctx.Err())
	stopErr := p.Stop(context.WithoutCancel(ctx))
	return finishRun(p, contextErr, stopErr)
}

func finishRun(p *Process, contextErr, stopErr error) (*Result, error) {
	failureErr := stopErr

	// A failed stop can return while the reaper is still running. Retry before
	// reading the file so that a successful recovery preserves its final output.
	select {
	case <-p.waitDone:
	default:
		if err := p.forceStopAndWait(); err != nil {
			failureErr = errors.Join(failureErr, fmt.Errorf("%w: %w", ErrCleanupFailed, err))
		}
	}

	result := &Result{Stdout: p.Stdout(), Stderr: p.Stderr()}
	p.mu.Lock()
	hasMemfd := p.memfd != nil && p.memfd.file != nil
	p.mu.Unlock()
	if hasMemfd {
		var outputErr error
		result.Memfd, outputErr = p.MemfdOutput()
		failureErr = errors.Join(failureErr, outputErr)
	}

	failureErr = errors.Join(failureErr, p.Close())
	p.mu.Lock()
	terminalErr := p.terminalErr
	startCancelErr := p.startCancelErr
	p.mu.Unlock()
	// A direct marker means launch cancellation had no independent failures.
	if startCancelErr != nil && terminalErr == startCancelErr { //nolint:errorlint // Compare the marker recorded by Start, not wrapped writer errors.
		contextErr = startCancelErr
	} else if contextErr == nil || terminalErr != ErrStopped { //nolint:errorlint // Only the direct lifecycle marker is an expected stop.
		failureErr = errors.Join(failureErr, terminalErr)
	}

	if failureErr != nil {
		return result, errors.Join(contextErr, failureErr)
	}

	switch {
	case errors.Is(contextErr, context.Canceled):
		return result, ErrRunCanceled
	case errors.Is(contextErr, context.DeadlineExceeded):
		return result, ErrRunTimeout
	default:
		return result, contextErr
	}
}
