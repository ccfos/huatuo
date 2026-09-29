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
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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

func TestRunUsesStopAsyncProfilerResultAfterCancellation(t *testing.T) {
	tests := []struct {
		name        string
		stopExit    int
		wantSuccess bool
		wantErr     string
	}{
		{name: "stop succeeds", wantSuccess: true},
		{name: "stop fails", stopExit: 23, wantErr: "exit status 23"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asprofPath := filepath.Join(t.TempDir(), "asprof")
			script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--libpath" ]; then
	exit %d
fi
trap 'exit 0' TERM
while :; do sleep 1; done
`, tt.stopExit)
			if err := os.WriteFile(asprofPath, []byte(script), 0o600); err != nil {
				t.Fatalf("write fake asprof: %v", err)
			}
			if err := os.Chmod(asprofPath, 0o700); err != nil {
				t.Fatalf("make fake asprof executable: %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()

			results := Run(ctx, []int{164879}, asprofPath, func(int) []string {
				return []string{"start"}
			})
			if len(results) != 1 {
				t.Fatalf("Run() returned %d results, want 1", len(results))
			}

			result := results[0]
			if result.Succeeded() != tt.wantSuccess {
				t.Errorf("Run() Succeeded=%t, want %t", result.Succeeded(), tt.wantSuccess)
			}
			if tt.wantErr == "" {
				if result.Err != nil {
					t.Errorf("Run() Err=%v, want nil", result.Err)
				}
				return
			}
			if result.Err == nil || !strings.Contains(result.Err.Error(), tt.wantErr) {
				t.Errorf("Run() Err=%v, want substring %q", result.Err, tt.wantErr)
			}
		})
	}
}

func TestRunStopsAsyncProfilerWithItsStartLibrary(t *testing.T) {
	dir := t.TempDir()
	asprofPath := filepath.Join(dir, "asprof")
	stopLibPath := filepath.Join(dir, "stop-libpath")
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--libpath" ]; then
	printf '%%s' "$2" > %q
	exit 0
fi
trap 'exit 0' TERM
while :; do sleep 1; done
`, stopLibPath)
	if err := os.WriteFile(asprofPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	libPath := "/tmp/libasyncProfiler-session123.so"

	results := Run(ctx, []int{1234}, asprofPath, func(int) []string {
		return []string{"start", "--libpath", libPath}
	})
	if len(results) != 1 || !results[0].Succeeded() {
		t.Fatalf("Run() results=%v, want successful stop", results)
	}
	got, err := os.ReadFile(stopLibPath)
	if err != nil || string(got) != libPath {
		t.Fatalf("stop --libpath=%q, error=%v, want %q", got, err, libPath)
	}
}

func TestRunDoesNotStopAsyncProfilerWhenLaunchIsCanceled(t *testing.T) {
	dir := t.TempDir()
	asprofPath := filepath.Join(dir, "asprof")
	stopMarker := filepath.Join(dir, "stop-called")
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--libpath" ]; then
	touch %q
fi
`, stopMarker)
	if err := os.WriteFile(asprofPath, []byte(script), 0o600); err != nil {
		t.Fatalf("write fake asprof: %v", err)
	}
	if err := os.Chmod(asprofPath, 0o700); err != nil {
		t.Fatalf("make fake asprof executable: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	results := Run(ctx, []int{164879}, asprofPath, func(int) []string {
		return []string{"start"}
	})
	if len(results) != 1 {
		t.Fatalf("Run() returned %d results, want 1", len(results))
	}
	if !errors.Is(results[0].Err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context cancellation", results[0].Err)
	}
	if _, err := os.Stat(stopMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("async-profiler stop marker error = %v, want missing file", err)
	}
}

func TestRunSeparatesProfilerOutputFromDiagnostics(t *testing.T) {
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

	results := Run(t.Context(), []int{164879}, toolPath, func(int) []string {
		return nil
	})
	if len(results) != 1 {
		t.Fatalf("Run() returned %d results, want 1", len(results))
	}
	result := results[0]
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

func TestRunWithMemfdKeepsPIDOutputsSeparate(t *testing.T) {
	results := RunWithMemfd(t.Context(), []int{123, 456}, "/bin/sh", func(pid int, outputPath string) []string {
		return []string{
			"-c", `printf 'process %s;work 3\n' "$1" > "$2"; printf 'status'; printf 'warning' >&2`,
			"memfd-test", strconv.Itoa(pid), outputPath,
		}
	})
	if len(results) != 2 {
		t.Fatalf("RunWithMemfd() returned %d results, want 2", len(results))
	}
	seen := make(map[int]bool)
	for _, result := range results {
		if result.Err != nil {
			t.Fatalf("RunWithMemfd() error = %v", result.Err)
		}
		if seen[result.PID] || (result.PID != 123 && result.PID != 456) {
			t.Fatalf("unexpected or repeated PID %d", result.PID)
		}
		seen[result.PID] = true
		want := fmt.Sprintf("process %d;work 3\n", result.PID)
		if string(result.Output) != want || string(result.Diagnostics) != "warning" {
			t.Errorf("PID %d: output = %q, diagnostics = %q", result.PID, result.Output, result.Diagnostics)
		}
		if !strings.Contains(result.Command, "/proc/self/fd/") {
			t.Errorf("command = %q, want inherited output path", result.Command)
		}
	}
}

func TestRunWithMemfdEnforcesProfilerLimit(t *testing.T) {
	results := RunWithMemfd(t.Context(), []int{123}, "/bin/sh", func(_ int, outputPath string) []string {
		return []string{
			"-c", `truncate -s "$1" "$2"`, "memfd-test", strconv.Itoa(profilerOutputLimit + 1), outputPath,
		}
	})
	if len(results) != 1 {
		t.Fatalf("RunWithMemfd() returned %d results, want 1", len(results))
	}
	result := results[0]
	if !errors.Is(result.Err, executil.ErrOutputLimitExceeded) {
		t.Fatalf("RunWithMemfd() error = %v, want size limit error", result.Err)
	}
	if len(result.Output) != 0 {
		t.Errorf("oversized output length = %d, want 0", len(result.Output))
	}
}
