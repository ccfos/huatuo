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

package formatter

import (
	"context"
	"strconv"
	"testing"

	"github.com/ccfos/huatuo/pkg/types"
)

func TestDisplayLabelsNamesSoftIRQVectors(t *testing.T) {
	tests := []struct {
		vec  uint32
		want string
	}{
		{
			vec:  0,
			want: "HI",
		},
		{
			vec:  1,
			want: "TIMER",
		},
		{
			vec:  2,
			want: "NET_TX",
		},
		{
			vec:  3,
			want: "NET_RX",
		},
		{
			vec:  4,
			want: "BLOCK",
		},
		{
			vec:  5,
			want: "IRQ_POLL",
		},
		{
			vec:  6,
			want: "TASKLET",
		},
		{
			vec:  7,
			want: "SCHED",
		},
		{
			vec:  8,
			want: "HRTIMER",
		},
		{
			vec:  9,
			want: "RCU",
		},
		{
			vec:  10,
			want: "10",
		},
		{
			vec:  99,
			want: "99",
		},
	}
	for _, tt := range tests {
		got := DisplayLabels(
			nil,
			[]types.ProfileLabel{types.ProfileStringLabel(
				"softirq_vector",
				strconv.FormatUint(
					uint64(tt.vec),
					10,
				),
			)},
		)
		if got[0] != "softirq_vector="+strconv.Quote(tt.want) {
			t.Fatal(got)
		}
	}
}

func TestWalkWindowRejectsInvalidReferences(t *testing.T) {
	window := &types.AggregationWindow{
		Aggregated: types.AggregatedProfile{
			Frames:  []types.ProfileFrame{{Name: "valid"}},
			Stacks:  [][]uint32{{1}},
			Samples: []types.AggregatedSample{{UserStack: 1}},
		},
	}
	if err := WalkWindow(context.Background(), window, func([]string, int64) error { return nil }); err == nil {
		t.Fatal("WalkWindow() accepted an out-of-range frame ID")
	}
}
