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

package exec

import (
	"errors"
	"strings"
	"testing"
)

func TestVerifyIncludesFailureDetails(t *testing.T) {
	t.Parallel()

	commandErr := errors.New("exit status 1")
	err := Verify([]*Result{
		{
			PID:         164879,
			Command:     "/usr/bin/tool trace 164879",
			Output:      []byte("partial profile\n"),
			Diagnostics: []byte("attach failed\n"),
			Err:         commandErr,
		},
	})
	if err == nil {
		t.Fatal("Verify() error = nil")
	}
	if !errors.Is(err, commandErr) {
		t.Fatalf("Verify() error = %v, want wrapped command error", err)
	}
	for _, want := range []string{
		`command "/usr/bin/tool trace 164879" failed for pid 164879`,
		"exit status 1",
		`diagnostics="attach failed"`,
		`output="partial profile"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Verify() error = %q, want substring %q", err, want)
		}
	}
}

func TestVerifyIncludesEveryFailure(t *testing.T) {
	t.Parallel()

	firstErr := errors.New("first failure")
	secondErr := errors.New("second failure")
	err := Verify([]*Result{
		{PID: 101, Command: "tool start 101", Err: firstErr},
		{PID: 202, Command: "tool start 202", Err: secondErr},
		{PID: 303, Command: "tool start 303"},
	})
	if err == nil {
		t.Fatal("Verify() error = nil")
	}
	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("Verify() error = %v, want both command errors", err)
	}
	for _, want := range []string{"pid 101", "pid 202"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Verify() error = %q, want substring %q", err, want)
		}
	}
}

func TestOutputForErrorTruncatesOversizedOutput(t *testing.T) {
	t.Parallel()

	output := []byte(strings.Repeat("x", maxOutputInError+1))
	got := outputForError(output)
	want := strings.Repeat("x", maxOutputInError) + "... (truncated)"
	if got != want {
		t.Fatalf("outputForError() length = %d, want %d", len(got), len(want))
	}
}
