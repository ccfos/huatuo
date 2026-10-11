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
	"os"
	"path/filepath"
	"testing"

	"github.com/ccfos/huatuo/internal/executil"
)

func TestFormatCommandIncludesExecutableAndArguments(t *testing.T) {
	t.Parallel()

	got := formatCommand(
		"/opt/async-profiler/bin/asprof",
		[]string{"dump", "-f", "/tmp/profile.collapsed", "164879"},
	)
	want := "/opt/async-profiler/bin/asprof dump -f /tmp/profile.collapsed 164879"
	if got != want {
		t.Fatalf("formatCommand()=%q, want %q", got, want)
	}
}

func TestRunCommandSeparatesProfilerOutputFromDiagnostics(t *testing.T) {
	toolPath := filepath.Join(t.TempDir(), "py-spy")
	script := `#!/bin/sh
printf 'profile output'
printf 'tool warning' >&2
`
	if err := os.WriteFile(toolPath, []byte(script), 0o600); err != nil {
		t.Fatalf("write fake profiler: %v", err)
	}
	if err := os.Chmod(toolPath, 0o700); err != nil {
		t.Fatalf("make fake profiler executable: %v", err)
	}

	result := runCommand(t.Context(), 164879, &executil.Spec{Path: toolPath})
	if !result.Succeeded() {
		t.Fatalf("Run() error = %v", result.Err)
	}
	if string(result.Output) != "profile output" {
		t.Fatalf("Run() output = %q, want %q", result.Output, "profile output")
	}
	if string(result.Diagnostics) != "tool warning" {
		t.Fatalf(
			"Run() diagnostics = %q, want %q",
			result.Diagnostics,
			"tool warning",
		)
	}
}
