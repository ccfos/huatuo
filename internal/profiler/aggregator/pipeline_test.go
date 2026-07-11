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

package aggregator

import (
	"context"
	"testing"
	"time"

	profctx "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/output"
)

func TestNewPipeline_DoesNotMutateContext(t *testing.T) {
	pctx := &profctx.ProfilerContext{}

	p := NewPipeline(pctx, NewMockAggregator(t))

	if pctx.AggrInterval != 0 {
		t.Fatalf("NewPipeline mutated AggrInterval to %d", pctx.AggrInterval)
	}

	if p.aggrInterval != 10*time.Second {
		t.Fatalf("NewPipeline aggrInterval = %s, want 10s", p.aggrInterval)
	}
}

func TestPipelineStart_IsIdempotent(t *testing.T) {
	aggr := NewMockAggregator(t)
	aggr.On("OutputFormatter").Return(nil).Once()

	p := NewPipeline(&profctx.ProfilerContext{
		Ctx:          context.Background(),
		OutputFormat: output.FormatCollapsed,
	}, aggr)

	p.Start()
	p.Start()
	p.Stop()
}

func TestPipelineStart_AfterStop(t *testing.T) {
	aggr := NewMockAggregator(t)

	p := NewPipeline(&profctx.ProfilerContext{
		Ctx:          context.Background(),
		OutputFormat: output.FormatCollapsed,
	}, aggr)

	p.Stop()
	p.Start()
	p.Enqueue("ignored")

	aggr.AssertNotCalled(t, "Aggregate")
}

func TestPipelineEnqueue_AfterStop(t *testing.T) {
	aggr := NewMockAggregator(t)
	aggr.On("OutputFormatter").Return(nil).Maybe()

	p := NewPipeline(&profctx.ProfilerContext{
		Ctx:          context.Background(),
		OutputFormat: output.FormatCollapsed,
	}, aggr)

	p.Stop()
	p.Enqueue("ignored")

	aggr.AssertNotCalled(t, "Aggregate")
}

func TestPipelineAggregateAndExport_NilFormatter(t *testing.T) {
	aggr := NewMockAggregator(t)
	aggr.On("OutputFormatter").Return(nil).Once()

	p := NewPipeline(&profctx.ProfilerContext{
		OutputFormat: output.FormatCollapsed,
	}, aggr)

	err := p.aggregateAndSnapshot(context.Background(), true)
	if err == nil {
		t.Fatal("aggregateAndSnapshot returned nil error")
	}

	const want = `output formatter is nil for non-upload format "collapsed"`
	if got := err.Error(); got != want {
		t.Fatalf("aggregateAndSnapshot error = %q, want %q", got, want)
	}
}

func TestPipelineAggregateAndExport_EmptyFormatter(t *testing.T) {
	formatter := NewFormatter(t)
	formatter.On("IsEmpty").Return(true).Once()

	aggr := NewMockAggregator(t)
	aggr.On("OutputFormatter").Return(formatter).Once()

	p := NewPipeline(&profctx.ProfilerContext{
		OutputFormat: output.FormatCollapsed,
		OutputPath:   t.TempDir(),
	}, aggr)

	if err := p.aggregateAndSnapshot(context.Background(), true); err != nil {
		t.Fatalf("aggregateAndSnapshot returned error: %v", err)
	}

	aggr.AssertNotCalled(t, "Reset")
}
