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
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/ccfos/huatuo/internal/profiler/output/flamegraph"
	_ "github.com/ccfos/huatuo/internal/profiler/output/raw"

	"github.com/ccfos/huatuo/pkg/profiling"

	"github.com/ccfos/huatuo/internal/profiler"
	"github.com/ccfos/huatuo/internal/profiler/output"

	"github.com/stretchr/testify/require"
)

func TestNormalizePythonOutput(t *testing.T) {
	raw := "worker (app.py:10) 3\n" +
		"process 101:\"python child.py\";child (child.py:4) 2\n" +
		"invalid\n"

	require.Equal(
		t,
		"process 100;worker (app.py:10) 3\n"+
			"process 101:\"python child.py\";child (child.py:4) 2\n",
		normalizePythonOutput(100, raw),
	)
}

func TestPythonAggregatorKeepsProcessRoots(t *testing.T) {
	cfg := &Config{Freq: 100}
	aggr, err := newPythonCPUAggregator(cfg)
	require.NoError(t, err)

	aggr.Aggregate(profiler.SampleOutput{PID: 100, Output: "hot (a.py:1) 3\n"})
	aggr.Aggregate(profiler.SampleOutput{PID: 200, Output: "hot (a.py:1) 4\n"})

	var folded bytes.Buffer
	window, err := aggr.Snapshot(t.Context())
	require.NoError(t, err)
	formatter, err := output.FormatCollapsed.NewRenderer()
	require.NoError(t, err)
	require.NoError(t, output.AddProfile(formatter, window.Profile))
	require.NoError(t, formatter.Write(&folded))
	require.Equal(
		t,
		"process 100;hot (a.py:1) 30000000\nprocess 200;hot (a.py:1) 40000000\n",
		folded.String(),
	)
	require.Equal(t, []profiler.SampleOutput{
		{PID: 100, Output: "process 100;hot (a.py:1) 3\n"},
		{PID: 200, Output: "process 200;hot (a.py:1) 4\n"},
	}, aggr.sampleOutput)
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
process 100;worker (app.py:10) 3
process 101;child (child.py:20) 2
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
			cfg := &Config{Freq: 100}
			aggr, err := newPythonCPUAggregator(cfg)
			require.NoError(t, err)

			p := &pythonCPUProfiler{
				config: Config{Duration: 5 * time.Second, Freq: 100, ToolDir: toolDir},
				pids:   []int{100},
			}
			require.NoError(t, p.ReadDataLoop(ctx, aggr.Aggregate))

			if format == output.FormatCollapsed {
				var folded bytes.Buffer
				window, err := aggr.Snapshot(t.Context())
				require.NoError(t, err)
				formatter, err := output.FormatCollapsed.NewRenderer()
				require.NoError(t, err)
				require.NoError(t, output.AddProfile(formatter, window.Profile))
				require.NoError(t, formatter.Write(&folded))
				require.Equal(t, "process 100;worker (app.py:10) 30000000\nprocess 101;child (child.py:20) 20000000\n", folded.String())
				return
			}

			snapshot, err := aggr.Snapshot(t.Context())
			require.NoError(t, err)
			data := snapshot
			require.Len(t, data.Profile.Sample, 2)
			var total int64
			for _, sample := range data.Profile.Sample {
				require.Len(t, sample.Value, 1)
				total += sample.Value[0]
			}
			require.Equal(t, int64(5)*int64(time.Second)/100, total)
			for _, frame := range []string{"process 100", "worker (app.py:10)", "process 101", "child (child.py:20)"} {
				require.Contains(t, data.Profile.StringTable, frame)
			}
			for _, frame := range data.Profile.StringTable {
				require.NotContains(t, frame, "py-spy>")
			}
		})
	}
}

func TestPythonAggregatorCapturesConfiguration(t *testing.T) {
	cfg := &Config{Type: profiling.TypeCPU, Freq: 100, ContainerID: "original-container"}
	aggr, err := newPythonCPUAggregator(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Type = "unsupported"
	cfg.Freq = 1
	cfg.ContainerID = "changed-container"
	window, err := aggr.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if window.ContainerID != "original-container" {
		t.Fatalf("container ID = %q", window.ContainerID)
	}
	if window.Profile.Period != 10000000 {
		t.Fatalf("sample period = %d, want 10000000", window.Profile.Period)
	}
}
