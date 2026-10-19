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

package window_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ccfos/huatuo/internal/profiling/profile"
	"github.com/ccfos/huatuo/internal/profiling/window"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestBuildProfilePreservesSamplesAndMetadata(t *testing.T) {
	for _, multiplier := range []int64{10000000, 1} {
		w := &types.AggregationWindow{
			ProfileType: profile.ProfileTypeCpuSample,
			Aggregated: types.AggregatedProfile{
				TimeNanos:     123,
				DurationNanos: 456,
				Frames:        []types.ProfileFrame{{Name: "main"}, {Name: "leaf"}, {Name: "other"}},
				Stacks:        [][]uint32{{0, 1}, {1}, {0}, {2}},
				Samples: []types.AggregatedSample{{
					UserStack: 1,
					Value:     7 * multiplier,
				}, {
					UserStack: 2,
					Value:     3 * multiplier,
				}, {
					UserStack:   3,
					KernelStack: 2,
					Value:       2 * multiplier,
				}, {
					UserStack: 4,
					Value:     -4,
				}, {
					UserStack: 4,
					Value:     4,
				}, {
					UserStack: 1,
					Value:     0,
				}},
			},
		}
		if err := window.Validate(w); err != nil {
			t.Fatal(err)
		}

		got, err := window.BuildProfile(t.Context(), w)
		if err != nil {
			t.Fatal(err)
		}

		for i, sample := range got.Sample {
			if sample.Value[0] != w.Aggregated.Samples[i].Value {
				t.Fatalf("value %v", sample.Value)
			}

			var names []string
			for _, id := range sample.LocationId {
				location := got.Location[id-1]
				names = append(names, got.StringTable[got.Function[location.Line[0].FunctionId-1].Name])
			}

			expected := [][]string{{"leaf", "main"}, {"leaf"}, {"leaf_[k]", "main"}, {"other"}, {"other"}, {"leaf", "main"}}
			if !reflect.DeepEqual(names, expected[i]) {
				t.Fatalf("frames %v want %v", names, expected[i])
			}
		}

		if got.TimeNanos != 123 || got.DurationNanos != 456 || got.Period != 0 {
			t.Fatalf("metadata %v", got)
		}
	}
}

func TestValidateRejectsInvalidWindows(t *testing.T) {
	for _, tc := range []struct {
		name   string
		modify func(*types.AggregationWindow)
	}{
		{
			name:   "missing stack",
			modify: func(w *types.AggregationWindow) { w.Aggregated.Samples = []types.AggregatedSample{{UserStack: 1}} },
		},
		{
			name:   "duration",
			modify: func(w *types.AggregationWindow) { w.Aggregated.DurationNanos = -1 },
		},
		{
			name:   "type",
			modify: func(w *types.AggregationWindow) { w.ProfileType = "cpu" },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &types.AggregationWindow{
				ProfileType: profile.ProfileTypeCpuSample,
				Aggregated:  types.AggregatedProfile{TimeNanos: 1},
			}
			tc.modify(w)
			if err := window.Validate(w); err == nil {
				t.Fatal("invalid window accepted")
			}
		})
	}
}

func TestBuildProfileCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := window.BuildProfile(ctx, &types.AggregationWindow{ProfileType: profile.ProfileTypeCpuSample})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error %v", err)
	}
}
