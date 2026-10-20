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

package speedscope

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/ccfos/huatuo/pkg/types"
)

func TestEncoderKeepsGroupsSeparate(t *testing.T) {
	e := encoder{
		unit:      "bytes",
		frames:    make(map[frame]int),
		frameList: []frame{},
	}
	inputs := []struct {
		container string
		profile   string
	}{
		{
			container: "a",
			profile:   "memory:physical_usage:bytes:space:bytes",
		},
		{
			container: "b",
			profile:   "memory:physical_usage:bytes:space:bytes",
		},
		{
			container: "a",
			profile:   "memory:physical_alloc:bytes:space:bytes",
		},
	}
	for _, input := range inputs {
		w := testWindow("bytes", 2)
		w.ContainerID = input.container
		w.ProfileType = input.profile
		w.Aggregated.LabelSets = [][]types.ProfileLabel{{types.ProfilePIDLabel(42)}}
		w.Aggregated.Samples[0].LabelSet = 1
		// Distinct stacks share a label set; an unlabeled zero sample keeps its own group.
		w.Aggregated.Samples = append(w.Aggregated.Samples, types.AggregatedSample{
			KernelStack: 1,
			LabelSet:    1,
			Value:       3,
		}, types.AggregatedSample{})
		if err := e.appendProfiles(t.Context(), w); err != nil {
			t.Fatal(err)
		}
	}
	var data bytes.Buffer
	if err := e.write(&data); err != nil {
		t.Fatal(err)
	}
	var got speedscopeFile
	if err := json.Unmarshal(data.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Profiles) != 6 {
		t.Fatalf("profiles = %d, want 6", len(got.Profiles))
	}
	for i, input := range inputs {
		for j := range 2 {
			profile := &got.Profiles[2*i+j]
			var name struct {
				Container string               `json:"container"`
				Profile   string               `json:"profile"`
				Labels    []types.ProfileLabel `json:"labels"`
			}
			if err := json.Unmarshal([]byte(profile.Name), &name); err != nil {
				t.Fatal(err)
			}
			if name.Container != input.container || name.Profile != input.profile {
				t.Fatalf("group metadata = %+v, want %+v", name, input)
			}
			if j == 0 {
				if len(name.Labels) != 1 || name.Labels[0] != types.ProfilePIDLabel(42) || profile.EndValue != 5 || len(profile.Samples) != 2 {
					t.Fatalf("labeled group = %+v, labels = %+v", profile, name.Labels)
				}
				if got.Shared.Frames[profile.Samples[0][0]].Name != "root" || got.Shared.Frames[profile.Samples[1][0]].Name != "root_[k]" {
					t.Fatal("user and kernel frames merged")
				}
			} else if name.Labels != nil || profile.EndValue != 0 || profile.Samples == nil || len(profile.Samples) != 0 || profile.Weights == nil || len(profile.Weights) != 0 {
				t.Fatalf("empty group = %+v, labels = %+v", profile, name.Labels)
			}
		}
	}
}
