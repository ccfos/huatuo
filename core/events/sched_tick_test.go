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

package events

import (
	"testing"

	"huatuo-bamai/internal/bpf/abi"
)

func TestSchedTickStackAddrs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		stackSize int64
		expected  int
	}{
		{
			name:      "stack capture failed",
			stackSize: -1,
		},
		{
			name: "empty stack",
		},
		{
			name:      "partial stack",
			stackSize: 3 * 8,
			expected:  3,
		},
		{
			name:      "unaligned stack size",
			stackSize: 3*8 + 7,
			expected:  3,
		},
		{
			name:      "oversized stack",
			stackSize: 1 << 30,
			expected:  len(abi.SchedTickEvent{}.Stack),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data := abi.SchedTickEvent{StackSize: tt.stackSize}
			got := schedTickStackAddrs(&data)
			if len(got) != tt.expected {
				t.Fatalf("schedTickStackAddrs() length = %d, want %d", len(got), tt.expected)
			}
		})
	}
}

func TestSchedTickAttachOptions(t *testing.T) {
	t.Parallel()

	expected := []struct {
		program string
		symbol  string
	}{
		{
			program: "trace_sched_tick_restart",
			symbol:  "tick_nohz_restart_sched_tick",
		},
		{
			program: "trace_sched_tick_stop",
			symbol:  "timer/tick_stop",
		},
		{
			program: "trace_sched_tick_interval",
			symbol:  "account_process_tick",
		},
	}

	got := schedTickAttachOptions("tick_nohz_restart_sched_tick")
	if len(got) != len(expected) {
		t.Fatalf("schedTickAttachOptions() length = %d, want %d", len(got), len(expected))
	}
	for i, opt := range got {
		if opt.ProgramName != expected[i].program || opt.Symbol != expected[i].symbol {
			t.Errorf(
				"schedTickAttachOptions()[%d] = (%q, %q), want (%q, %q)",
				i,
				opt.ProgramName,
				opt.Symbol,
				expected[i].program,
				expected[i].symbol,
			)
		}
	}
}
