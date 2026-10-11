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

var (
	// ErrCmdCanceled reports Run cancellation without independent failures.
	ErrCmdCanceled = errors.New("executil: command canceled")
	// ErrCmdTimeout reports Run timeout without independent failures.
	ErrCmdTimeout = errors.New("executil: command timed out")
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

// RunError separates cancellation from independent failures. errors.Is and
// errors.As inspect all causes. Expected signal exits caused by cancellation
// are omitted; nonzero exits and output drain failures remain in ExecutionErr.
type RunError struct {
	ContextErr   error
	ExecutionErr error
	OutputErr    error
	CleanupErr   error
	// Process transfers recovery ownership when Run's final Close fails.
	// Retry Close before discarding it. Wait still observes the single reaper;
	// a group cleanup failure after reaping remains an error, since signaling
	// the retired PID again would risk killing an unrelated process group.
	Process *Process
}

func (e *RunError) Error() string {
	if err := errors.Join(e.Unwrap()...); err != nil {
		return err.Error()
	}
	return ""
}

// Unwrap preserves every independent failure for errors.Is and errors.As.
func (e *RunError) Unwrap() []error {
	var causes []error
	for _, err := range []error{e.ContextErr, e.ExecutionErr, e.OutputErr, e.CleanupErr} {
		if err != nil {
			causes = append(causes, err)
		}
	}
	return causes
}

func (e *RunError) isCancellation() bool {
	return e.ContextErr != nil && e.ExecutionErr == nil && e.OutputErr == nil && e.CleanupErr == nil
}

// Run executes, captures output and closes one command. It always returns a
// non-nil Result, which is empty for invalid configuration. Other failures
// return the available output snapshots.
// Pure cancellation and timeout errors match ErrCmdCanceled and ErrCmdTimeout,
// respectively, after output collection and cleanup finish.
// Cancellation is an error. A failed stop gets a final forceful cleanup attempt;
// earlier stop failures remain reported even if recovery succeeds. A failed
// final Close returns its Process in RunError for explicit recovery by the caller.
func Run(ctx context.Context, spec Spec, options ...Option) (*Result, error) { //nolint:gocritic // Preserve value semantics for command configuration.
	p, err := New(spec, options...)
	if err != nil {
		return &Result{}, &RunError{ExecutionErr: err}
	}

	return collectResult(p, p.Run(ctx))
}

func collectResult(p *Process, runErr error) (*Result, error) {
	failure := &RunError{}
	if runErr != nil {
		var classified *RunError
		if errors.As(runErr, &classified) {
			*failure = *classified
		} else {
			failure.ExecutionErr = runErr
		}
	}

	// A failed stop can return while the reaper is still running. Retry before
	// reading the file so that a successful recovery preserves its final output.
	select {
	case <-p.wait.done:
	default:
		failure.CleanupErr = errors.Join(failure.CleanupErr,
			wrapStopFailure(p.forceStopAndWait()))
	}

	result := &Result{}
	result.Stdout = p.Stdout()
	result.Stderr = p.Stderr()
	p.mu.Lock()
	hasMemfd := p.memfd != nil && p.memfd.file != nil
	p.mu.Unlock()
	if hasMemfd {
		var outputErr error
		result.Memfd, outputErr = p.MemfdOutput()
		failure.OutputErr = errors.Join(failure.OutputErr, outputErr)
	}

	if err := p.Close(); err != nil {
		failure.CleanupErr = errors.Join(failure.CleanupErr, err)
		failure.Process = p
	}
	p.mu.Lock()
	waitErr := p.wait.err
	if errors.Is(p.start.err, waitErr) || (failure.ContextErr != nil && errors.Is(waitErr, ErrStopped)) {
		waitErr = nil
	}
	if waitErr != nil && !errors.Is(failure.ExecutionErr, waitErr) {
		failure.ExecutionErr = errors.Join(failure.ExecutionErr, waitErr)
	}
	p.mu.Unlock()
	if len(failure.Unwrap()) == 0 {
		return result, nil
	}
	if failure.isCancellation() {
		switch {
		case errors.Is(failure.ContextErr, context.Canceled):
			return result, errors.Join(ErrCmdCanceled, failure)
		case errors.Is(failure.ContextErr, context.DeadlineExceeded):
			return result, errors.Join(ErrCmdTimeout, failure)
		}
	}
	return result, failure
}

// Run starts the command and waits for it. If ctx is canceled after launch,
// Spec.StopGracePeriod controls when Stop escalates from SIGTERM to SIGKILL.
// Run preserves memfd output, including after cancellation. Read outputs and
// call Close to release the file. MemfdOutput reports read limit errors.
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
