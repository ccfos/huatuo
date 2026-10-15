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

package registry

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/profiler/aggregator"
	pcontext "github.com/ccfos/huatuo/internal/profiler/context"
	"github.com/ccfos/huatuo/internal/profiler/output"
)

type exportResultAggregator struct {
	fakeAggregator
	result any
	err    error
}

func (a exportResultAggregator) Snapshot(*pcontext.ProfilerContext) (any, error) {
	return a.result, a.err
}

// TestProfileReturnsExportFailure 验证采样循环正常结束后仍会返回导出错误；
// 覆盖无数据正常完成、快照转换失败以及上传客户端不可用三个场景。
func TestProfileReturnsExportFailure(t *testing.T) {
	conversionErr := errors.New("profile conversion failed")
	cases := []struct {
		name     string
		aggr     exportResultAggregator
		validate func(*testing.T, error)
	}{
		{name: "empty-result", validate: func(t *testing.T, err error) {
			if err != nil {
				t.Errorf("Profile() = %v", err)
			}
		}},
		{name: "snapshot-failure", aggr: exportResultAggregator{err: conversionErr}, validate: func(t *testing.T, err error) {
			if !errors.Is(err, conversionErr) {
				t.Errorf("Profile() = %v, want conversion failure", err)
			}
		}},
		{name: "upload-failure", aggr: exportResultAggregator{result: "profile-payload"}, validate: func(t *testing.T, err error) {
			if err == nil || !strings.Contains(err.Error(), "toolstream client not initialized") {
				t.Errorf("Profile() = %v, want upload failure", err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			pctx := &pcontext.ProfilerContext{Ctx: ctx, Cancel: cancel, IsOneShotAgg: true, OutputFormat: output.FormatRemote}
			meta := ProfilerMeta{Impl: fakeProfiler{}, NewAggregator: func(*pcontext.ProfilerContext) (aggregator.Aggregator, error) { return tc.aggr, nil }}
			tc.validate(t, Profile(pctx, meta))
		})
	}
}
