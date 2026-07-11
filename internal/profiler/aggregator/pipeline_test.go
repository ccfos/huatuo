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
	"io"
	"testing"

	profctx "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/output"
)

func TestPipelineAggregateAndExport_NilFormatter(t *testing.T) {
	aggr := NewMockAggregator(t)
	aggr.On("OutputFormatter").Return(nil).Once()

	p := NewPipeline(&profctx.ProfilerContext{
		OutputFormat: output.FormatCollapsed,
	}, aggr)

	err := p.aggregateAndExport(context.Background(), true)
	if err == nil {
		t.Fatal("aggregateAndExport returned nil error")
	}

	const want = `output formatter is nil for non-upload format "collapsed"`
	if got := err.Error(); got != want {
		t.Fatalf("aggregateAndExport error = %q, want %q", got, want)
	}
}

func TestPipelineAggregateAndExport_EmptyFormatter(t *testing.T) {
	aggr := NewMockAggregator(t)
	aggr.On("OutputFormatter").Return(&mockFormatter{empty: true}).Once()

	p := NewPipeline(&profctx.ProfilerContext{
		OutputFormat: output.FormatCollapsed,
		OutputPath:   t.TempDir(),
	}, aggr)

	if err := p.aggregateAndExport(context.Background(), true); err != nil {
		t.Fatalf("aggregateAndExport returned error: %v", err)
	}

	aggr.AssertNotCalled(t, "Reset")
}

type mockFormatter struct {
	empty bool
}

func (mf *mockFormatter) Name() string {
	return "mock"
}

func (mf *mockFormatter) Add(*output.Sample) error {
	return nil
}

func (mf *mockFormatter) Write(io.Writer) error {
	return nil
}

func (mf *mockFormatter) Reset() {}

func (mf *mockFormatter) IsEmpty() bool {
	return mf.empty
}
