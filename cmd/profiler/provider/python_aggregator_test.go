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

	"github.com/ccfos/huatuo/internal/profiler"
	pcontext "github.com/ccfos/huatuo/internal/profiler/context"
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
	pctx := &pcontext.ProfilerContext{Freq: 99, OutputFormat: output.FormatCollapsed}
	aggr, err := newPythonCPUAggregator(pctx)
	require.NoError(t, err)

	aggr.Aggregate(profiler.SampleOutput{PID: 100, Output: "hot (a.py:1) 3\n"})
	aggr.Aggregate(profiler.SampleOutput{PID: 200, Output: "hot (a.py:1) 4\n"})

	var folded bytes.Buffer
	require.NoError(t, aggr.OutputFormatter().Write(&folded))
	require.Equal(
		t,
		"process 100;hot (a.py:1) 3\nprocess 200;hot (a.py:1) 4\n",
		folded.String(),
	)
	require.Equal(t, []profiler.SampleOutput{
		{PID: 100, Output: "process 100;hot (a.py:1) 3\n"},
		{PID: 200, Output: "process 200;hot (a.py:1) 4\n"},
	}, aggr.sampleOutput)
}

func TestNormalizePythonOutputSkipsStatusLines(t *testing.T) {
	raw := "py-spy> Sampling process 99 times a second for 5 seconds. Press Control-C to exit.\n" +
		"worker (app.py:10) 3\n" +
		"  py-spy> Wrote raw flamegraph data to '/dev/stdout'. Samples: 3 Errors: 2\r\n" +
		"py-spy> Wrote raw flamegraph data to '/dev/stdout'. Samples: 3 Errors: 0\n" +
		"process 101;py-spy> callback (app.py:20) 2\n"

	require.Equal(t,
		"process 100;worker (app.py:10) 3\nprocess 101;py-spy> callback (app.py:20) 2\n",
		normalizePythonOutput(100, raw),
	)
}

func TestRunPySpyAndEmitExcludesStatusSamples(t *testing.T) {
	toolDir := t.TempDir()
	// py-spy 0.4.1 exits successfully even when individual samples fail.
	// Its stdout contains both raw stacks and this error-count summary.
	script := `#!/bin/sh
cat <<'RAW'
py-spy> Sampling process 100 times a second for 5 seconds. Press Control-C to exit.
process 100;worker (app.py:10) 3
process 101;child (child.py:20) 2
py-spy> Wrote raw flamegraph data to '/dev/stdout'. Samples: 5 Errors: 325
py-spy> You can use the flamegraph.pl script to generate a SVG
RAW
`
	toolPath := filepath.Join(toolDir, "py-spy")
	require.NoError(t, os.WriteFile(toolPath, []byte(script), 0o600))
	require.NoError(t, os.Chmod(toolPath, 0o700))

	for _, format := range []output.OutputFormat{output.FormatCollapsed, output.FormatRemote} {
		t.Run(string(format), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			pctx := &pcontext.ProfilerContext{Ctx: ctx, Freq: 100, OutputFormat: format}
			aggr, err := newPythonCPUAggregator(pctx)
			require.NoError(t, err)

			require.NoError(t, runPySpyAndEmit(ctx, 5, 100, toolDir, []int{100}, aggr.Aggregate))

			if format == output.FormatCollapsed {
				var folded bytes.Buffer
				require.NoError(t, aggr.OutputFormatter().Write(&folded))
				require.Equal(t, "process 100;worker (app.py:10) 3\nprocess 101;child (child.py:20) 2\n", folded.String())
				return
			}

			snapshot, err := aggr.Snapshot(pctx)
			require.NoError(t, err)
			data, ok := snapshot.(*profiler.ProfileData)
			require.True(t, ok, "snapshot type: %T", snapshot)
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
