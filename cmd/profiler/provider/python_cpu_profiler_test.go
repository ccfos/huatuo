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

package provider

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ccfos/huatuo/internal/profiler"
	pcontext "github.com/ccfos/huatuo/internal/profiler/context"

	"github.com/stretchr/testify/require"
)

func TestResolvePythonPidsExplicitTargets(t *testing.T) {
	pctx := &pcontext.ProfilerContext{PIDs: []int{123, 456}}

	pids, err := resolvePythonPids(pctx)
	require.NoError(t, err)
	require.Equal(t, []int{123, 456}, pids)
}

func TestPythonRootPids(t *testing.T) {
	parents := map[int]int{
		100: 1,
		101: 100,
		102: 101,
		200: 1,
		201: 200,
	}
	parentPID := func(pid int) (int, error) {
		return parents[pid], nil
	}

	roots, err := pythonRootPids([]int{100, 101, 102, 200, 201}, parentPID)
	require.NoError(t, err)
	require.Equal(t, []int{100, 200}, roots)
}

func TestPythonRootPidsDetectsParentCycle(t *testing.T) {
	parents := map[int]int{100: 200, 200: 100}
	parentPID := func(pid int) (int, error) {
		return parents[pid], nil
	}

	_, err := pythonRootPids([]int{100}, parentPID)
	require.EqualError(t, err, "resolve Python target PID 100 ancestry: process parent cycle at PID 100")
}

func TestBuildPySpyArgs(t *testing.T) {
	require.Equal(t, []string{
		"record",
		"-d", "10",
		"-f", "raw",
		"-r", "99",
		"--subprocesses",
		"-o", "/proc/self/fd/3",
		"-p", "123",
	}, buildPySpyArgs(123, "10", "99", "/proc/self/fd/3"))
}

func TestRunPySpyAndEmitUsesOnlyMemfdSamples(t *testing.T) {
	tests := []struct {
		name    string
		sample  string
		exit    int
		wantErr string
	}{
		{name: "samples with status messages", sample: "work (app.py:10) 3\n"},
		{name: "no samples", wantErr: "no samples collected"},
		{name: "whitespace", sample: " \n", wantErr: "no samples collected"},
		{name: "failed command", sample: "partial 3\n", exit: 7, wantErr: "exit status 7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toolDir := writeFakePySpy(t, tt.sample, tt.exit)
			var samples []profiler.SampleOutput
			err := runPySpyAndEmit(t.Context(), 1, 99, toolDir, []int{123, 456}, func(sample any) {
				output, ok := sample.(profiler.SampleOutput)
				require.True(t, ok, "unexpected sample type %T", sample)
				samples = append(samples, output)
			})
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				if tt.exit != 0 {
					require.ErrorContains(t, err, "tool warning")
				}
				require.Empty(t, samples)
				return
			}
			require.NoError(t, err)
			require.ElementsMatch(t, []profiler.SampleOutput{
				{PID: 123, Output: tt.sample},
				{PID: 456, Output: tt.sample},
			}, samples)
		})
	}
}

func TestRunPySpyPreservesExitError(t *testing.T) {
	results := runPySpy(t.Context(), []int{123}, 1, 99, writeFakePySpy(t, "partial 3\n", 7))
	require.Len(t, results, 1)
	var exitErr *exec.ExitError
	if !errors.As(results[0].Err, &exitErr) || exitErr.ExitCode() != 7 {
		t.Fatalf("runPySpy() error = %v, want exit status 7", results[0].Err)
	}
	require.Empty(t, results[0].Output)
	require.Equal(t, "tool warning\n", string(results[0].Diagnostics))
}

func writeFakePySpy(t *testing.T, sample string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	toolPath := filepath.Join(dir, "py-spy")
	script := fmt.Sprintf(`#!/bin/sh
set -eu
while [ "$#" -gt 0 ]; do
	if [ "$1" = '-o' ]; then
		output=$2
		break
	fi
	shift
done
printf '%%s' '%s' > "$output"
printf 'py-spy> Sampling process 99 times a second. Press Control-C to exit.\n'
printf 'py-spy> Wrote raw flamegraph data. Samples: 0 Errors: 272\n'
printf 'py-spy> You can use the flamegraph.pl script to generate a SVG\n'
printf 'tool warning\n' >&2
exit %d
`, sample, exitCode)
	if err := os.WriteFile(toolPath, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(toolPath, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestValidatePythonAggregationWindow(t *testing.T) {
	require.NoError(t, validatePythonAggregationWindow(10, 10))
	require.EqualError(
		t,
		validatePythonAggregationWindow(30, 10),
		"Python CPU profiler does not support continuous profiling: aggregation interval (10s) must equal duration (30s)",
	)
}
