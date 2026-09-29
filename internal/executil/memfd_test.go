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

func TestStopPreservesMemfdAndGroupCleanupErrors(t *testing.T) {
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
			process.state = processStateExited
			process.memfd = &memfdOutput{closeErr: closeErr}
			process.groupErr = wrapStopFailure(cleanupErr)
			close(process.start.done)
			close(process.wait.done)
			for range 2 {
				err := process.Stop(t.Context())
				if !errors.Is(err, closeErr) {
					t.Fatalf("Stop() error = %v, want stored close error", err)
				}

				if errors.Is(err, ErrStopFailed) != (cleanupErr != nil) {
					t.Fatalf("Stop() error = %v, incorrect process group failure classification", err)
				}

				if cleanupErr != nil && !errors.Is(err, cleanupErr) {
					t.Fatalf("Stop() error = %v, want group cleanup error", err)
				}
			}

			if err := process.Wait(); errors.Is(err, closeErr) {
				t.Fatalf("Wait() included a later file close error: %v", err)
			}
		})
	}
}

func TestNewRejectsNonPositiveMemfdLimit(t *testing.T) {
	for _, limit := range []int{0, -1} {
		_, err := New(Spec{Path: "/unused/command"}, WithMemfdOutput(limit, func(string) []string { return nil }))
		if err == nil || !strings.Contains(err.Error(), "must be positive") {
			t.Fatalf("New() with limit %d error = %v, want positive limit error", limit, err)
		}
	}
}

func TestReadMemfdOutput(t *testing.T) {
	for _, data := range []string{"", "123", "1234", "12345"} {
		t.Run(data, func(t *testing.T) {
			reader := bytes.NewReader([]byte(data))
			for range 2 {
				got, err := readMemfdOutput(reader, int64(len(data)), 4)
				if errors.Is(err, ErrOutputLimitExceeded) != (len(data) > 4) {
					t.Fatalf("readMemfdOutput() error = %v for %d bytes", err, len(data))
				}

				if string(got) != data[:min(len(data), 4)] {
					t.Fatalf("readMemfdOutput() = %q, want retained prefix", got)
				}

				if len(got) > 0 {
					got[0] = 'x'
				}
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
	for _, state := range []processState{processStateNew, processStateStarting, processStateExited} {
		process.state = state
		_, err := process.MemfdOutput()
		if err == nil {
			t.Fatalf("MemfdOutput() in state %d succeeded", state)
		}

		if state == processStateExited && !errors.Is(err, os.ErrClosed) {
			t.Fatalf("MemfdOutput() error = %v, want os.ErrClosed", err)
		}
	}
}
