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
	"testing"

	"huatuo-bamai/internal/profiler"
	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/output"
)

func TestPythonMemoryAggregatorRetainsNetAllocations(t *testing.T) {
	pctx := &pcontext.ProfilerContext{
		Ctx:          context.Background(),
		OutputFormat: output.FormatCollapsed,
	}
	aggr, err := newPythonMemoryAggregator(pctx)
	if err != nil {
		t.Fatal(err)
	}
	if !pctx.IsOneShotAgg {
		t.Fatal("Memray aggregator did not enable one-shot aggregation")
	}

	aggr.Aggregate(profiler.SampleOutput{
		PID: 123,
		Output: "process 123:python;foo 10\n" +
			"process 123:python;foo -4\n" +
			"process 123:python;bar -3\n" +
			"process 123:python;gone 5\n" +
			"process 123:python;gone -5\n",
	})

	formatter := aggr.OutputFormatter()
	var output bytes.Buffer
	if err := formatter.Write(&output); err != nil {
		t.Fatal(err)
	}

	const want = "process 123:python;foo 6\n"
	if got := output.String(); got != want {
		t.Fatalf("folded output = %q, want %q", got, want)
	}
}

func TestPythonMemoryAggregatorReset(t *testing.T) {
	pctx := &pcontext.ProfilerContext{
		Ctx:          context.Background(),
		OutputFormat: output.FormatCollapsed,
	}
	aggr, err := newPythonMemoryAggregator(pctx)
	if err != nil {
		t.Fatal(err)
	}
	aggr.Aggregate(profiler.SampleOutput{Output: "foo 1\n"})
	aggr.Reset()

	if !aggr.OutputFormatter().IsEmpty() {
		t.Fatal("formatter is not empty after reset")
	}
}

func TestPythonMemoryAggregatorSnapshotsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pctx := &pcontext.ProfilerContext{
		Ctx:          ctx,
		PIDs:         []int{123},
		OutputFormat: output.FormatRemote,
	}
	aggr, err := newPythonMemoryAggregator(pctx)
	if err != nil {
		t.Fatal(err)
	}
	aggr.Aggregate(profiler.SampleOutput{
		PID:    123,
		Output: "process 123:python;foo 10\n",
	})

	cancel()
	profile, err := aggr.Snapshot(pctx)
	if err != nil {
		t.Fatalf("snapshot after profiler cancellation: %v", err)
	}
	if profile == nil {
		t.Fatal("snapshot after profiler cancellation is nil")
	}
}
