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
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/executil"
)

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

func TestRunWithMemfdDiscardsOutputAfterCommandFailure(t *testing.T) {
	results := RunWithMemfd(t.Context(), []int{123}, "/bin/sh", func(_ int, path string) []string {
		return []string{"-c", `printf partial > "$1"; printf failure >&2; exit 7`, "test", path}
	})
	if len(results) != 1 || results[0].Err == nil || len(results[0].Output) != 0 || string(results[0].Diagnostics) != "failure" {
		t.Fatalf("results: %+v", results)
	}
}
