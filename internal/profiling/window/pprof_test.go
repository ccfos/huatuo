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
	"reflect"
	"testing"

	"github.com/ccfos/huatuo/internal/profiling/profile"
	"github.com/ccfos/huatuo/internal/profiling/window"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestBuildProfilePreservesUserAndKernelFrames(t *testing.T) {
	t.Parallel()
	w := &types.AggregationWindow{
		ProfileType: profile.ProfileTypeCpuSample,
		Aggregated: types.AggregatedProfile{
			TimeNanos: 1,
			Frames: []types.ProfileFrame{{
				Name: "shared",
				File: "source.c",
				Line: 42,
			}},
			Stacks: [][]uint32{{0}},
			Samples: []types.AggregatedSample{{
				UserStack: 1,
				Value:     2,
			}, {
				KernelStack: 1,
				Value:       3,
			}, {
				UserStack:   1,
				KernelStack: 1,
				Value:       5,
			}},
		},
	}
	p, err := window.BuildProfile(t.Context(), w)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Sample) != 3 {
		t.Fatalf("sample count = %d, want 3", len(p.Sample))
	}
	expectedNames := [][]string{{"shared"}, {"shared_[k]"}, {"shared_[k]", "shared"}}
	for i, sample := range p.Sample {
		var names []string
		for _, id := range sample.LocationId {
			location := p.Location[id-1]
			line := location.Line[0]
			function := p.Function[line.FunctionId-1]
			names = append(names, p.StringTable[function.Name])
			if p.StringTable[function.SystemName] != p.StringTable[function.Name] ||
				p.StringTable[function.Filename] != "source.c" || line.Line != 42 {
				t.Fatalf("sample %d lost function metadata: %v, %v", i, function, line)
			}
		}
		if !reflect.DeepEqual(names, expectedNames[i]) {
			t.Errorf("sample %d frames = %v, want %v", i, names, expectedNames[i])
		}
		if !reflect.DeepEqual(sample.Value, []int64{w.Aggregated.Samples[i].Value}) {
			t.Errorf("sample %d value = %v", i, sample.Value)
		}
	}
	userID := p.Sample[0].LocationId[0]
	kernelID := p.Sample[1].LocationId[0]
	if userID == kernelID {
		t.Error("user and kernel frames share a location")
	}
	if !reflect.DeepEqual(p.Sample[2].LocationId, []uint64{kernelID, userID}) {
		t.Errorf("mixed stack locations = %v, want kernel then user locations", p.Sample[2].LocationId)
	}
	if w.Aggregated.Frames[0].Name != "shared" {
		t.Fatal("BuildProfile modified the input frame dictionary")
	}
}
