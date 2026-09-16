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
	"testing"

	"huatuo-bamai/internal/utils/executil"
)

func TestRetrySampleProfiler(t *testing.T) {
	commandErr := errors.New("failed")
	tests := []struct {
		name      string
		ctx       context.Context
		results   []executil.CmdResult
		wantCalls int
		wantErr   error
		wantOK    bool
	}{
		{name: "returns successful sample", ctx: context.Background(), results: []executil.CmdResult{{Success: true}}, wantCalls: 1, wantOK: true},
		{name: "retries busy result then succeeds", ctx: context.Background(), results: []executil.CmdResult{{Stderr: []byte(ProfilerBusyMsg)}, {Success: true}}, wantCalls: 2, wantOK: true},
		{name: "returns non busy command error", ctx: context.Background(), results: []executil.CmdResult{{CmdErr: commandErr}}, wantCalls: 1, wantErr: commandErr},
		{name: "stops retry when context is cancelled", ctx: cancelledContext(), results: []executil.CmdResult{{Stderr: []byte(ProfilerBusyMsg)}}, wantCalls: 1, wantErr: context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			resultIndex := 0
			result := RetrySampleProfiler(tt.ctx, 42, 1, 99, "/tmp/async-profiler", "collapsed", func(context.Context, []int, int, int, string, string) []executil.CmdResult {
				calls++
				current := tt.results[resultIndex]
				if resultIndex < len(tt.results)-1 {
					resultIndex++
				}
				return []executil.CmdResult{current}
			})
			if calls != tt.wantCalls {
				t.Errorf("sample calls = %d, want %d", calls, tt.wantCalls)
			}
			if !errors.Is(result.CmdErr, tt.wantErr) {
				t.Errorf("CmdErr = %v, want %v", result.CmdErr, tt.wantErr)
			}
			if result.Success != tt.wantOK {
				t.Errorf("Success = %t, want %t", result.Success, tt.wantOK)
			}
		})
	}
}

func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}
