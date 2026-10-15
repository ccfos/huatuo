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

	profctx "github.com/ccfos/huatuo/internal/profiler/context"
	"github.com/ccfos/huatuo/internal/profiler/output"
)

type finalSnapshotAggregator struct {
	snapshot func(*profctx.ProfilerContext) (any, error)
	records  []any
}

func (a *finalSnapshotAggregator) Aggregate(record any) { a.records = append(a.records, record) }
func (a *finalSnapshotAggregator) Snapshot(ctx *profctx.ProfilerContext) (any, error) {
	return a.snapshot(ctx)
}
func (*finalSnapshotAggregator) Reset()                            {}
func (*finalSnapshotAggregator) OutputFormatter() output.Formatter { return nil }

// TestPipelineFinalSnapshot 验证采样取消后最终快照仍能读取完整数据；
// 覆盖单次与周期聚合，保留 context 值、排空队列且不修改采样器原 context。
func TestPipelineFinalSnapshot(t *testing.T) {
	for _, oneShot := range []bool{true, false} {
		name := "periodic"
		if oneShot {
			name = "one-shot"
		}
		t.Run(name, func(t *testing.T) {
			type contextKey struct{}
			ctx, cancel := context.WithCancel(context.WithValue(t.Context(), contextKey{}, "profile-final-export"))
			defer cancel()
			pctx := &profctx.ProfilerContext{Ctx: ctx, IsOneShotAgg: oneShot, OutputFormat: output.FormatRemote, TracerID: "profile-final-export"}
			snapshots := 0
			aggr := &finalSnapshotAggregator{}
			aggr.snapshot = func(got *profctx.ProfilerContext) (any, error) {
				snapshots++
				if err := got.Ctx.Err(); err != nil {
					t.Errorf("final snapshot context canceled: %v", err)
				}
				if got.Ctx.Value(contextKey{}) != "profile-final-export" {
					t.Errorf("snapshot lost context value")
				}
				if len(aggr.records) != 1 {
					t.Errorf("snapshot records = %d, want 1", len(aggr.records))
				}
				return nil, nil
			}
			pipe := NewPipeline(pctx, aggr)
			pipe.Enqueue("sample-main-worker")
			pipe.Start()
			cancel()
			if err := pipe.Stop(); err != nil {
				t.Errorf("Stop() = %v", err)
			}
			if snapshots != 1 {
				t.Errorf("snapshots = %d, want 1", snapshots)
			}
			if pctx.Ctx != ctx || !errors.Is(pctx.Ctx.Err(), context.Canceled) {
				t.Errorf("sampler context was replaced or uncanceled")
			}
		})
	}
}

// TestPipelineStopReturnsExportError 验证最终快照失败能够由 Stop 返回，
// 并确认重复停止保留同一错误，不会将失败任务变成成功。
func TestPipelineStopReturnsExportError(t *testing.T) {
	want := errors.New("profile conversion failed")
	aggr := &finalSnapshotAggregator{snapshot: func(*profctx.ProfilerContext) (any, error) { return nil, want }}
	pipe := NewPipeline(&profctx.ProfilerContext{Ctx: t.Context(), IsOneShotAgg: true, OutputFormat: output.FormatRemote}, aggr)
	pipe.Start()
	for range 2 {
		if err := pipe.Stop(); !errors.Is(err, want) {
			t.Errorf("Stop() = %v, want %v", err, want)
		}
	}
}
