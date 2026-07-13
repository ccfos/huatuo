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

func TestVerifyResultsIncludesFailureDetails(t *testing.T) {
	t.Parallel()

	cmdErr := errors.New("exit status 1")
	err := VerifyResults([]CmdResult{
		{
			Pid:     164879,
			Cmd:     "/opt/async-profiler/bin/asprof start 164879",
			Stdout:  []byte("diagnostic output\n"),
			Stderr:  []byte("attach failed\n"),
			Success: false,
			CmdErr:  cmdErr,
		},
	})
	if err == nil {
		t.Fatal("VerifyResults() error=nil, want non-nil")
	}
	if !errors.Is(err, cmdErr) {
		t.Fatalf("VerifyResults() error=%v, want wrapped command error", err)
	}

	for _, want := range []string{
		`command "/opt/async-profiler/bin/asprof start 164879" failed for pid 164879`,
		"exit status 1",
		`stderr="attach failed"`,
		`stdout="diagnostic output"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("VerifyResults() error=%q, want substring %q", err, want)
		}
	}
}

func TestVerifyResultsIncludesEveryFailure(t *testing.T) {
	t.Parallel()

	firstErr := errors.New("first failure")
	secondErr := errors.New("second failure")
	err := VerifyResults([]CmdResult{
		{Pid: 101, Cmd: "asprof start 101", CmdErr: firstErr},
		{Pid: 202, Cmd: "asprof start 202", CmdErr: secondErr},
		{Pid: 303, Cmd: "asprof start 303", Success: true},
	})
	if err == nil {
		t.Fatal("VerifyResults() error=nil, want non-nil")
	}
	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("VerifyResults() error=%v, want both command errors", err)
	}
	for _, want := range []string{"pid 101", "pid 202"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("VerifyResults() error=%q, want substring %q", err, want)
		}
	}
}
