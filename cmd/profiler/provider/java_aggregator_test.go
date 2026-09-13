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

func TestJavaAggregatorSplitsCollapsedFrames(t *testing.T) {
	pctx := &pcontext.ProfilerContext{Freq: 99, OutputFormat: output.FormatCollapsed}
	aggr, err := newJavaAggregator(pctx)
	require.NoError(t, err)

	aggr.Aggregate(profiler.SampleOutput{
		PID:    1234,
		Output: "HotCode.main;HotCode.hotMethod;java/util/UUID.randomUUID 42\n",
	})
	aggr.Aggregate(profiler.SampleOutput{
		PID:    1234,
		Output: "HotCode.main;HotCode.hotMethod;java/util/UUID.randomUUID 8\n",
	})

	var folded bytes.Buffer
	require.NoError(t, aggr.OutputFormatter().Write(&folded))
	require.Equal(
		t,
		"process 1234;HotCode.main;HotCode.hotMethod;java/util/UUID.randomUUID 50\n",
		folded.String(),
	)
}
