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

package java

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"huatuo-bamai/internal/utils/executil"
)

// samplerOf records invocations and returns a canned result.
type samplerOf struct {
	calls   int
	results [][]executil.CmdResult
}

func (s *samplerOf) sample(_ context.Context, _ []int, _, _ int, _, _ string) []executil.CmdResult {
	if s.calls < len(s.results) {
		res := s.results[s.calls]
		s.calls++
		return res
	}
	s.calls++
	return nil
}

func TestRetrySampleProfilerEmptySamplerResult(t *testing.T) {
	sampler := &samplerOf{results: [][]executil.CmdResult{nil}}
	result := RetrySampleProfiler(
		context.Background(), 42, 1, 1, "/tools", "collapsed", sampler.sample,
	)
	if sampler.calls != 1 {
		t.Errorf("sampler calls=%d, want 1 (empty result must not be retried)", sampler.calls)
	}
	if result.Success {
		t.Error("Success=true, want false for empty sampler result")
	}
	if result.CmdErr == nil || !strings.Contains(result.CmdErr.Error(), "no result") {
		t.Errorf("CmdErr=%v, want empty-result failure", result.CmdErr)
	}
}

func TestRetrySampleProfilerPreCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	sampler := &samplerOf{}
	result := RetrySampleProfiler(
		ctx, 42, 1, 1, "/tools", "collapsed", sampler.sample,
	)
	if sampler.calls != 0 {
		t.Errorf("sampler calls=%d, want 0 (canceled context must not reach sampler)", sampler.calls)
	}
	if !errors.Is(result.CmdErr, context.Canceled) {
		t.Errorf("CmdErr=%v, want context.Canceled", result.CmdErr)
	}
}

func TestRetrySampleProfilerRetriesBusyThenSucceeds(t *testing.T) {
	busy := executil.CmdResult{
		Pid:     42,
		Stderr:  []byte(ProfilerBusyMsg),
		CmdErr:  errors.New(ProfilerBusyMsg),
		Success: false,
	}
	done := executil.CmdResult{Pid: 42, Success: true}

	sampler := &samplerOf{results: [][]executil.CmdResult{
		{busy},
		{done},
	}}
	startedAt := time.Now()
	result := RetrySampleProfiler(
		context.Background(), 42, 1, 1, "/tools", "collapsed", sampler.sample,
	)

	if sampler.calls != 2 {
		t.Errorf("sampler calls=%d, want 2 (busy then success)", sampler.calls)
	}
	if !result.Success {
		t.Errorf("Success=false, want true; err=%v", result.CmdErr)
	}
	if elapsed := time.Since(startedAt); elapsed < retryInterval {
		t.Errorf("second attempt ran after %v, want at least one retry delay", elapsed)
	}
}
