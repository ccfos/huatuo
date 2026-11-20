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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestNewRejectsNilMemfdArgumentBuilder(t *testing.T) {
	_, err := New(Spec{Path: "/unused/command"}, WithMemfdOutput(4, nil))
	if err == nil || !strings.Contains(err.Error(), "output argument builder must not be nil") {
		t.Fatalf("New() error = %v, want nil argument builder error", err)
	}
}

func TestNewRejectsInvalidMemfdLimit(t *testing.T) {
	for _, limit := range []int{0, -1} {
		process, err := New(Spec{Path: "/unused/command"}, WithMemfdOutput(limit, func(string) []string {
			return nil
		}))
		if process != nil || err == nil || !strings.Contains(err.Error(), "maximum memfd output bytes must be positive") {
			t.Fatalf("New() with memfd limit %d = (%v, %v), want invalid limit error", limit, process, err)
		}
	}
}

func TestMemfdPrepareClosesFileWhenArgumentsAreInvalid(t *testing.T) {
	output := &memfdOutput{argsForOutput: func(string) []string { return []string{"bad\x00arg"} }}
	if err := output.prepare(&exec.Cmd{}); err == nil {
		t.Fatal("prepare() accepted an argument containing a NUL byte")
	}
	if output.file != nil {
		t.Fatal("prepare() retained the memfd after argument validation failed")
	}
}

func TestMemfdOptionDefersCreationUntilStart(t *testing.T) {
	calls := 0
	option := WithMemfdOutput(4, func(string) []string {
		calls++
		return nil
	})
	first, err := New(Spec{Path: "/unused/command"}, option)
	if err != nil {
		t.Fatal(err)
	}

	second, err := New(Spec{Path: "/unused/command"}, option)
	if err != nil {
		t.Fatal(err)
	}

	if first.memfd == second.memfd {
		t.Fatal("reused option shares file ownership across processes")
	}

	hasAllocatedFile := first.memfd.file != nil || second.memfd.file != nil
	if hasAllocatedFile || calls != 0 {
		t.Fatal("New() allocated a file or invoked the argument builder")
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = first.Start(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() error = %v, want context.Canceled", err)
	}

	if first.memfd.file != nil || calls != 0 {
		t.Fatal("canceled Start() allocated a file or invoked the argument builder")
	}

	if err := first.Stop(t.Context()); err != nil {
		t.Fatalf("Stop() after canceled Start() error = %v", err)
	}
}

func TestReleaseResourcesPreservesLifecycleAndCachesFailure(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "owned-output")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	process, err := New(Spec{Path: "/unused/command"})
	if err != nil {
		t.Fatal(err)
	}
	// A starting lifecycle must remain unpublished while its resources close.
	process.state = processRunStateStarting
	process.terminalErr = context.Canceled
	process.memfd = &memfdOutput{file: file}
	cleanupErr := releaseResources(process)
	var pathErr *os.PathError
	if !errors.Is(cleanupErr, ErrCleanupFailed) || !errors.Is(cleanupErr, os.ErrClosed) || !errors.As(cleanupErr, &pathErr) {
		t.Fatalf("releaseResources() lost cleanup classification or cause: %v", cleanupErr)
	}
	if process.memfd.file != nil {
		t.Fatal("releaseResources() retained the file")
	}
	if got := releaseResources(process); got != cleanupErr { //nolint:errorlint // Cached identity allows cleanup reports to be deduplicated.
		t.Fatalf("second releaseResources() = %v, want cached error %v", got, cleanupErr)
	}
	if process.state != processRunStateStarting || process.terminalErr != context.Canceled { //nolint:errorlint // Resource release must preserve the launch marker itself.
		t.Fatal("releaseResources() changed the lifecycle result")
	}
	for _, done := range []<-chan struct{}{process.startDone, process.waitDone, process.Done()} {
		select {
		case <-done:
			t.Fatal("releaseResources() published lifecycle completion")
		default:
		}
	}
}

func TestCloseReportsMemfdFailureSeparatelyFromReaperFailure(t *testing.T) {
	closeErr := errors.New("close memfd failed")
	groupErr := errors.New("terminate group failed")
	for _, cleanupErr := range []error{nil, groupErr} {
		name := "file only"
		if cleanupErr != nil {
			name = "file and process group"
		}

		t.Run(name, func(t *testing.T) {
			process, err := New(Spec{Path: "/unused/command"})
			if err != nil {
				t.Fatal(err)
			}
			// Publish failures without requiring an OS close or signal failure.
			process.state = processRunStateFinished
			process.memfd = &memfdOutput{closeErr: closeErr}
			if cleanupErr != nil {
				process.terminalErr = fmt.Errorf("%w: %w", ErrCleanupFailed, errors.Join(ErrStopFailed, cleanupErr))
			}
			close(process.startDone)
			close(process.waitDone)
			process.publishDone()
			for range 2 {
				err := process.Close()
				if !errors.Is(err, closeErr) {
					t.Fatalf("Close() error = %v, want stored close error", err)
				}

				if errors.Is(err, ErrStopFailed) {
					t.Fatalf("Close() error = %v, incorrect process group failure classification", err)
				}

				if cleanupErr != nil && !errors.Is(process.Wait(), cleanupErr) {
					t.Fatalf("Wait() lost group cleanup error: %v", process.Wait())
				}
			}

			if err := process.Wait(); errors.Is(err, closeErr) {
				t.Fatalf("Wait() included a later file close error: %v", err)
			}
		})
	}
}

func TestMemfdOutputRead(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		wantErr error
	}{
		{name: "empty"},
		{name: "below limit", data: "123"},
		{name: "at limit", data: "1234"},
		{name: "above limit", data: "12345", wantErr: ErrOutputLimitExceeded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file, err := os.CreateTemp(t.TempDir(), "memfd-output")
			if err != nil {
				t.Fatal(err)
			}
			output := &memfdOutput{file: file, limit: 4}
			t.Cleanup(func() {
				if err := output.close(); err != nil {
					t.Error(err)
				}
			})
			if _, err := file.WriteString(tt.data); err != nil {
				t.Fatal(err)
			}

			for range 2 {
				got, err := output.read()
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("read() error = %v, want %v", err, tt.wantErr)
				}

				if string(got) != tt.data[:min(len(tt.data), 4)] {
					t.Fatalf("read() = %q, want retained prefix", got)
				}

				if len(got) > 0 {
					got[0] = 'x'
				}
			}

			if offset, err := file.Seek(0, io.SeekCurrent); err != nil || offset != int64(len(tt.data)) {
				t.Fatalf("file offset after read() = (%d, %v), want %d", offset, err, len(tt.data))
			}
			if err := output.close(); err != nil {
				t.Fatal(err)
			}
			if _, err := output.read(); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("read() after close() error = %v, want os.ErrClosed", err)
			}
		})
	}
}

func TestReadMemfdOutputPreservesLimitAndReadErrors(t *testing.T) {
	data, err := readMemfdOutput(bytes.NewReader([]byte("a")), 5, 4)
	hasBothErrors := errors.Is(err, ErrOutputLimitExceeded) && errors.Is(err, io.EOF)
	if string(data) != "a" || !hasBothErrors {
		t.Fatalf("readMemfdOutput() = (%q, %v), want partial data, limit and read errors", data, err)
	}
}

func TestMemfdOutputRejectsUnavailableFile(t *testing.T) {
	var zero Process
	if _, err := zero.MemfdOutput(); !errors.Is(err, ErrProcessNotInitialized) {
		t.Fatalf("zero Process.MemfdOutput() error = %v", err)
	}

	process, err := New(Spec{Path: "/unused/command"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := process.MemfdOutput(); err == nil {
		t.Fatal("MemfdOutput() without WithMemfdOutput succeeded")
	}

	process.memfd = &memfdOutput{limit: 4}
	for _, state := range []processState{processRunStateNew, processRunStateStarting, processRunStateFinished} {
		process.state = state
		_, err := process.MemfdOutput()
		if err == nil {
			t.Fatalf("MemfdOutput() in state %d succeeded", state)
		}

		if state == processRunStateFinished && !errors.Is(err, os.ErrClosed) {
			t.Fatalf("MemfdOutput() error = %v, want os.ErrClosed", err)
		}
	}
}

func TestCloseDoesNotChangePublishedExecutionError(t *testing.T) {
	process, err := New(Spec{Path: "/unused/command"})
	if err != nil {
		t.Fatal(err)
	}
	executionErr := errors.New("execution failed")
	fileErr := errors.New("file close failed")
	process.state = processRunStateFinished
	process.terminalErr = executionErr
	process.memfd = &memfdOutput{closeErr: fileErr}
	close(process.startDone)
	close(process.waitDone)
	process.publishDone()
	for range 2 {
		closeErr := process.Close()
		if errors.Is(closeErr, executionErr) || !errors.Is(closeErr, fileErr) {
			t.Fatalf("Close() lost a cause: %v", closeErr)
		}
		cleanupErr := closeErr
		if !errors.Is(cleanupErr, fileErr) || errors.Is(cleanupErr, executionErr) {
			t.Fatalf("incorrect cleanup classification: %v", cleanupErr)
		}
		if got := process.Wait(); got != executionErr { //nolint:errorlint // Verify Close leaves the published object unchanged.
			t.Fatalf("Wait() changed after Close: %v", got)
		}
	}
}

func TestCloseDoesNotRepeatFailedStartCleanup(t *testing.T) {
	process, err := New(Spec{Path: "/unused/command"})
	if err != nil {
		t.Fatal(err)
	}
	fileErr := errors.New("file close failed")
	process.memfd = &memfdOutput{closeErr: fileErr}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	startErr := process.Start(ctx)
	if !errors.Is(startErr, context.Canceled) || !errors.Is(startErr, ErrCleanupFailed) || !errors.Is(startErr, fileErr) {
		t.Fatalf("Start lost cancellation or cleanup failure: %v", startErr)
	}
	for range 2 {
		if err := process.Close(); err != nil {
			t.Fatalf("Close repeated a cleanup failure owned by Start: %v", err)
		}
		if got := process.Wait(); got != startErr { //nolint:errorlint // Closing must preserve the published lifecycle result.
			t.Fatalf("Wait() = %v, want published result %v", got, startErr)
		}
	}
}
