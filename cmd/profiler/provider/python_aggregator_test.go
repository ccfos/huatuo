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
	"testing"

	"huatuo-bamai/internal/profiler"
	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/output"

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
