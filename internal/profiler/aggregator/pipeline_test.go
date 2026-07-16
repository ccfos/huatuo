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
	"errors"
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

func TestResolveTracerID(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		allocate   func() (string, error)
		want       string
		wantAlloc  bool
	}{
		{
			name:       "configured ID",
			configured: "trace-123",
			allocate: func() (string, error) {
				return "unexpected", nil
			},
			want: "trace-123",
		},
		{
			name: "allocated ID",
			allocate: func() (string, error) {
				return "generated-123", nil
			},
			want:      "generated-123",
			wantAlloc: true,
		},
		{
			name: "allocation error",
			allocate: func() (string, error) {
				return "", errors.New("random source unavailable")
			},
			wantAlloc: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allocated := false
			allocate := func() (string, error) {
				allocated = true
				return tt.allocate()
			}
			if got := resolveTracerID(tt.configured, allocate); got != tt.want {
				t.Fatalf("resolveTracerID() = %q, want %q", got, tt.want)
			}
			if allocated != tt.wantAlloc {
				t.Fatalf("allocator called = %t, want %t", allocated, tt.wantAlloc)
			}
		})
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

func TestPipelineStop_DrainsAcceptedRecords(t *testing.T) {
	aggr := NewMockAggregator(t)
	aggr.On("Aggregate", "first").Once()
	aggr.On("Aggregate", "second").Once()
	aggr.On("OutputFormatter").Return(nil).Once()

	p := NewPipeline(&profctx.ProfilerContext{
		Ctx:          t.Context(),
		OutputFormat: output.FormatCollapsed,
	}, aggr)
	p.Enqueue("first")
	p.Enqueue("second")

	p.Start()
	p.Stop()
}

func TestPipelineEnqueue_CountsOverflow(t *testing.T) {
	p := NewPipeline(&profctx.ProfilerContext{}, NewMockAggregator(t))

	for i := 0; i < pipelineQueueCapacity+1; i++ {
		p.Enqueue(i)
	}

	if got := len(p.queue); got != pipelineQueueCapacity {
		t.Fatalf("len(p.queue) = %d, want %d", got, pipelineQueueCapacity)
	}
	if got := p.overflowCount.Load(); got != 1 {
		t.Fatalf("p.overflowCount.Load() = %d, want 1", got)
	}
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
