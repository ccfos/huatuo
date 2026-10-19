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
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/profiling/aggregation"

	output "github.com/ccfos/huatuo/internal/profiling/formatter"

	"github.com/ccfos/huatuo/internal/profiling/pipeline"
	"github.com/ccfos/huatuo/internal/profiling/profile"
	"github.com/ccfos/huatuo/pkg/profiling"
	"github.com/ccfos/huatuo/pkg/types"

	"github.com/stretchr/testify/require"
)

func TestResolvePythonPidsExplicitTargets(t *testing.T) {
	cfg := &Config{PIDs: []int{123, 456}}

	pids, err := resolvePythonPids(cfg)
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
		"-f", "raw",
		"-r", "99",
		"--subprocesses",
		"-o", "/proc/self/fd/3",
		"-p", "123",
	}, buildPySpyArgs(
		123,
		"99",
		"/proc/self/fd/3",
	))
}

func TestPythonCPUProfilerReadDataLoopUsesOnlyMemfdSamples(t *testing.T) {
	tests := []struct {
		name    string
		sample  string
		exit    int
		wantErr string
	}{
		{
			name:   "samples with status messages",
			sample: "work (app.py:10) 3\n",
		},
		{
			name:    "no samples",
			wantErr: "no samples collected",
		},
		{
			name:    "whitespace",
			sample:  " \n",
			wantErr: "no samples collected",
		},
		{
			name:    "failed command",
			sample:  "partial 3\n",
			exit:    7,
			wantErr: "exit status 7",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toolDir := writeFakePySpy(t, tt.sample, tt.exit)
			p := &pythonCPUProfiler{
				config: Config{
					Duration: 1 * time.Second,
					Freq:     99,
					ToolDir:  toolDir,
				},
				pids: []int{123, 456},
			}
			var samples []aggregation.Sample
			err := p.ReadDataLoop(
				t.Context(),
				func(batch *pipeline.SampleBatch) error { samples = append(samples, batch.Samples...); return nil },
			)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				if tt.exit != 0 {
					require.ErrorContains(t, err, "tool warning")
				}

				require.Empty(t, samples)

				return
			}

			require.NoError(t, err)
			require.Len(t, samples, 2)
			for _, sample := range samples {
				require.Equal(t, []types.ProfileFrame{{
					Name: "work",
					File: "app.py",
					Line: 10,
				}}, sample.UserFrames)
				require.Equal(t, int64(3*(1000000000/99)), sample.Value)
			}
		})
	}
}

func TestRunPySpyPreservesExitError(t *testing.T) {
	results := runPySpy(
		t.Context(),
		[]int{123},
		99,
		writeFakePySpy(t, "partial 3\n", 7),
	)
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
	require.NoError(t, validatePythonAggregationWindow(10*time.Second, 10*time.Second))
	require.EqualError(
		t,
		validatePythonAggregationWindow(30*time.Second, 10*time.Second),
		"Python CPU profiler does not support continuous profiling: aggregation interval (10s) must equal duration (30s)",
	)
}

func TestPythonCPUProfilerRejectsFractionalDuration(t *testing.T) {
	for _, tc := range []struct {
		name     string
		duration time.Duration
	}{
		{name: "zero"}, {
			name:     "negative",
			duration: -time.Second,
		}, {
			name:     "fractional",
			duration: 1500 * time.Millisecond,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &pythonCPUProfiler{config: Config{
				Duration:            tc.duration,
				AggregationInterval: tc.duration,
				ToolDir:             writeFakePySpy(t, "", 0),
			}}
			err := p.Start(t.Context())
			require.ErrorContains(t, err, "duration must be a positive whole number of seconds")
		})
	}
}

func TestPythonSamplesKeepProcessLabels(t *testing.T) {
	var samples []aggregation.Sample
	for _, pid := range []int{100, 200} {
		weight := 3
		if pid == 200 {
			weight = 4
		}

		require.NoError(t, enqueueFolded(
			profile.SampleOutput{
				PID:    pid,
				Output: fmt.Sprintf("hot (a.py:1) %d\n", weight),
			},
			[]types.ProfileLabel{types.ProfilePIDLabel(uint32(pid))},
			10000000,
			profiling.LanguagePython,
			func(batch *pipeline.SampleBatch) error { samples = append(samples, batch.Samples...); return nil },
		))
	}

	require.Len(t, samples, 2)
	require.Equal(t, "100", samples[0].Labels[0].StringValue)
	require.Equal(t, "200", samples[1].Labels[0].StringValue)
	require.Equal(t, int64(30000000), samples[0].Value)
	require.Equal(t, int64(40000000), samples[1].Value)
}

func TestPythonCPUProfilerReadDataLoopExcludesStatusSamples(t *testing.T) {
	toolDir := t.TempDir()
	// py-spy 0.4.1 exits successfully even when individual samples fail.
	// Raw stacks go to the requested file; the error-count summary stays on stdout.
	script := `#!/bin/sh
set -eu
while [ "$#" -gt 0 ]; do
  if [ "$1" = '-o' ]; then
    output=$2
    break
  fi
  shift
done
cat > "$output" <<'SAMPLES'
process 100:"parent";worker (app.py:10) 3
process 101:"child";child (child.py:20) 2
SAMPLES
cat <<'RAW'
py-spy> Sampling process 100 times a second for 5 seconds. Press Control-C to exit.
py-spy> Wrote raw flamegraph data. Samples: 5 Errors: 325
py-spy> You can use the flamegraph.pl script to generate a SVG
RAW
`
	toolPath := filepath.Join(toolDir, "py-spy")
	require.NoError(t, os.WriteFile(toolPath, []byte(script), 0o600))
	require.NoError(t, os.Chmod(toolPath, 0o700))

	for _, format := range []output.OutputFormat{output.FormatCollapsed, output.FormatRemoteStore} {
		t.Run(string(format), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var samples []aggregation.Sample
			p := &pythonCPUProfiler{
				config: Config{
					Duration: 5 * time.Second,
					Freq:     100,
					ToolDir:  toolDir,
				},
				pids:   []int{100},
				labels: map[int][]types.ProfileLabel{100: {types.ProfilePIDLabel(100)}},
			}
			require.NoError(
				t,
				p.ReadDataLoop(
					ctx,
					func(batch *pipeline.SampleBatch) error { samples = append(samples, batch.Samples...); return nil },
				),
			)
			require.Len(t, samples, 2)
			require.Equal(t, int64(50000000), samples[0].Value+samples[1].Value)
			for i, sample := range samples {
				require.Len(t, sample.UserFrames, 1)
				expected := []types.ProfileFrame{{
					Name: "worker",
					File: "app.py",
					Line: 10,
				}, {
					Name: "child",
					File: "child.py",
					Line: 20,
				}}
				require.Equal(t, expected[i], sample.UserFrames[0])
				require.Contains(t, sample.Labels, types.ProfilePIDLabel(uint32(100+i)))
			}
		})
	}
}

func TestPythonProfilerCapturesConfiguration(t *testing.T) {
	cfg := &Config{
		Type:        profiling.TypeCPU,
		Freq:        100,
		ContainerID: "original-container",
	}
	task, err := New(profiling.ImplementationPython, cfg)
	if err != nil {
		t.Fatal(err)
	}

	cfg.Freq = 1
	cfg.ContainerID = "changed-container"
	if task.AggregationConfig.ContainerID != "original-container" || task.Impl.(*pythonCPUProfiler).config.Freq != 100 {
		t.Fatal("task aliases configuration")
	}
}
