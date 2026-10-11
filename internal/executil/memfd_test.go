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
	"io"
	"os"
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

func TestClosePreservesMemfdAndGroupCleanupErrors(t *testing.T) {
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
			process.state = processStateFinished
			process.memfd = &memfdOutput{closeErr: closeErr}
			process.groupErr = wrapStopFailure(cleanupErr)
			close(process.start.done)
			close(process.wait.done)
			process.publishDone()
			for range 2 {
				err := process.Close()
				if !errors.Is(err, closeErr) {
					t.Fatalf("Close() error = %v, want stored close error", err)
				}

				if errors.Is(err, ErrStopFailed) != (cleanupErr != nil) {
					t.Fatalf("Close() error = %v, incorrect process group failure classification", err)
				}

				if cleanupErr != nil && !errors.Is(err, cleanupErr) {
					t.Fatalf("Close() error = %v, want group cleanup error", err)
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
	if _, err := zero.MemfdOutput(); !errors.Is(err, errProcessNotInitialized) {
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
	for _, state := range []processState{processStateNew, processStateStarting, processStateFinished} {
		process.state = state
		_, err := process.MemfdOutput()
		if err == nil {
			t.Fatalf("MemfdOutput() in state %d succeeded", state)
		}

		if state == processStateFinished && !errors.Is(err, os.ErrClosed) {
			t.Fatalf("MemfdOutput() error = %v, want os.ErrClosed", err)
		}
	}
}
