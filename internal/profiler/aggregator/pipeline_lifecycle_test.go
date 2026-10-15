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
	"runtime"
	"sync/atomic"
	"testing"

	profctx "github.com/ccfos/huatuo/internal/profiler/context"
	"github.com/ccfos/huatuo/internal/profiler/output"
)

type lifecycleEmptyFormatter struct{}

func (*lifecycleEmptyFormatter) Name() string             { return "audit" }
func (*lifecycleEmptyFormatter) Add(*output.Sample) error { return nil }
func (*lifecycleEmptyFormatter) Write(io.Writer) error    { return nil }
func (*lifecycleEmptyFormatter) Reset()                   {}
func (*lifecycleEmptyFormatter) IsEmpty() bool            { return true }

type lifecycleAggregator struct{ snapshots atomic.Int64 }

func (*lifecycleAggregator) Aggregate(any)                                  {}
func (*lifecycleAggregator) Snapshot(*profctx.ProfilerContext) (any, error) { return nil, nil }
func (*lifecycleAggregator) Reset()                                         {}
func (a *lifecycleAggregator) OutputFormatter() output.Formatter {
	a.snapshots.Add(1)
	return &lifecycleEmptyFormatter{}
}

func TestPipelineStopWaitsForConcurrentStart(t *testing.T) {
	for i := 0; i < 2000; i++ {
		aggr := &lifecycleAggregator{}
		p := NewPipeline(&profctx.ProfilerContext{
			Ctx: context.Background(), OutputFormat: output.FormatCollapsed,
		}, aggr)
		started := make(chan struct{})
		go func() { p.Start(); close(started) }()
		for p.state.Load() == pipelineStateIdle {
			runtime.Gosched()
		}
		p.Stop()
		got := aggr.snapshots.Load()
		<-started
		p.wg.Wait()
		if got != 1 {
			t.Fatalf("iteration %d: Stop returned before final snapshot, count=%d", i, got)
		}
	}
}
