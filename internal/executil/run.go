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
)

// Result owns output snapshots that remain valid after the command is closed.
// Redirected streams are empty. Memfd is empty unless WithMemfdOutput was used.
// Failed commands can return partial output; a limit error makes it incomplete.
type Result struct {
	Stdout          []byte
	Stderr          []byte
	Memfd           []byte
	StderrTruncated bool
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

// IsCancellation reports cancellation or a deadline with no independent failure.
// Callers decide whether either context error means a successful business stop.
func (e *RunError) IsCancellation() bool {
	return e.ContextErr != nil && e.ExecutionErr == nil && e.OutputErr == nil && e.CleanupErr == nil
}

// Run executes, captures output and closes one command. Invalid configuration
// returns a nil Result; all other failures return the available snapshots.
// Cancellation is an error. A failed stop gets a final forceful cleanup attempt;
// earlier stop failures remain reported even if recovery succeeds. A failed
// final Close returns its Process in RunError for explicit recovery by the caller.
func Run(ctx context.Context, spec Spec, options ...Option) (*Result, error) { //nolint:gocritic // Spec is within the 80-byte value limit.
	process, err := New(spec, options...)
	if err != nil {
		return nil, &RunError{ExecutionErr: err}
	}

	return collectResult(process, process.Run(ctx))
}

func collectResult(process *Process, runErr error) (*Result, error) {
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
	case <-process.wait.done:
	default:
		failure.CleanupErr = errors.Join(failure.CleanupErr,
			wrapStopFailure(process.forceStopAndWait(process.wait.done)))
	}

	result := &Result{}
	result.Stdout, failure.OutputErr = process.Stdout()
	result.Stderr, result.StderrTruncated = process.stderr.Snapshot()
	process.mu.Lock()
	hasMemfd := process.memfd != nil && process.memfd.file != nil
	process.mu.Unlock()
	if hasMemfd {
		var outputErr error
		result.Memfd, outputErr = process.MemfdOutput()
		failure.OutputErr = errors.Join(failure.OutputErr, outputErr)
	}

	if err := process.Close(); err != nil {
		failure.CleanupErr = errors.Join(failure.CleanupErr, err)
		failure.Process = process
	}
	process.mu.Lock()
	waitErr := process.wait.err
	if errors.Is(process.start.err, waitErr) || (failure.ContextErr != nil && errors.Is(waitErr, ErrStopped)) {
		waitErr = nil
	}
	if waitErr != nil && !errors.Is(failure.ExecutionErr, waitErr) {
		failure.ExecutionErr = errors.Join(failure.ExecutionErr, waitErr)
	}
	process.mu.Unlock()
	if len(failure.Unwrap()) == 0 {
		return result, nil
	}
	return result, failure
}
