// Copyright 2026 The HuaTuo Authors
// Copyright 2026 The Ascend Authors
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

package hccn

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunHCCNCommandCapturesStdout(t *testing.T) {
	tool := writeTestTool(t, "printf 'link status UP'")

	output, err := runHCCNCommand(t.Context(), tool)
	if err != nil {
		t.Fatalf("runHCCNCommand() error = %v", err)
	}
	if output != "link status UP" {
		t.Fatalf("runHCCNCommand() output = %q, want %q", output, "link status UP")
	}
}

func TestRunHCCNCommandIncludesStderrOnFailure(t *testing.T) {
	tool := writeTestTool(t, "printf 'driver failed' >&2; exit 1")

	_, err := runHCCNCommand(t.Context(), tool)
	if err == nil || !strings.Contains(err.Error(), "driver failed") {
		t.Fatalf("runHCCNCommand() error = %v, want stderr output", err)
	}
}

func TestRunHCCNCommandLabelsBothStreamsOnFailure(t *testing.T) {
	tool := writeTestTool(t, "printf 'query failed'; printf 'driver failed' >&2; exit 1")

	_, err := runHCCNCommand(t.Context(), tool)
	if err == nil {
		t.Fatal("runHCCNCommand() error = nil")
	}
	for _, expected := range []string{`stdout="query failed"`, `stderr="driver failed"`} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("runHCCNCommand() error = %v, want contain %q", err, expected)
		}
	}
}

func TestHCCNCommandErrorLimitsOutput(t *testing.T) {
	err := hccnCommandError(
		nil,
		bytes.Repeat([]byte("o"), maxHCCNErrorBytes),
		[]byte("stderr tail"),
		errors.New("command failed"),
	)
	if !strings.Contains(err.Error(), "(truncated)") {
		t.Fatalf("hccnCommandError() = %v, want truncation marker", err)
	}
	if strings.Contains(err.Error(), strings.Repeat("o", maxHCCNErrorBytes)) {
		t.Fatalf("hccnCommandError() retained the full stdout error output")
	}
}

func writeTestTool(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hccn_tool")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o600); err != nil {
		t.Fatalf("write test tool: %v", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatalf("make test tool executable: %v", err)
	}
	return path
}
