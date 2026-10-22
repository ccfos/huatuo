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
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/ccfos/huatuo/pkg/types"
)

func TestEncoderKeepsGroupsSeparate(t *testing.T) {
	e := encoder{
		unit:      "bytes",
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
	if err := e.write(t.Context(), &data); err != nil {
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

func TestEncoderFrameIdentity(t *testing.T) {
	e := encoder{
		unit: "bytes",
	}
	w := testWindow("bytes", 1)
	w.Aggregated.Frames = []types.ProfileFrame{{Name: "work_[k]"}, {Name: "work"}}
	w.Aggregated.Stacks = [][]uint32{{0}, {1}}
	w.Aggregated.Samples = []types.AggregatedSample{
		{
			UserStack: 1,
			Value:     1,
		},
		{
			KernelStack: 2,
			Value:       2,
		},
		{
			UserStack: 2,
			Value:     3,
		},
		{
			UserStack:   1,
			KernelStack: 2,
			Value:       4,
		},
	}
	if err := e.appendProfiles(t.Context(), w); err != nil {
		t.Fatal(err)
	}

	// Dictionary IDs change across windows; semantic frame IDs must not.
	w.ContainerID = "other"
	w.Aggregated.Frames[0], w.Aggregated.Frames[1] = w.Aggregated.Frames[1], w.Aggregated.Frames[0]
	w.Aggregated.Stacks = [][]uint32{{1}, {0}}
	if err := e.appendProfiles(t.Context(), w); err != nil {
		t.Fatal(err)
	}

	var data bytes.Buffer
	if err := e.write(t.Context(), &data); err != nil {
		t.Fatal(err)
	}
	var got speedscopeFile
	if err := json.Unmarshal(data.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Shared.Frames) != 3 || len(got.Profiles) != 2 {
		t.Fatalf("frame identity lost: %+v", got)
	}
	if got.Shared.Frames[0].Name != "work_[k]" || got.Shared.Frames[1].Name != "work_[k]" {
		t.Fatalf("display names changed: %+v", got.Shared.Frames)
	}
	for i := range got.Profiles {
		if diff := cmp.Diff([][]int{{0}, {1}, {2}, {0, 1}}, got.Profiles[i].Samples); diff != "" {
			t.Fatalf("samples (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff([]int64{1, 2, 3, 4}, got.Profiles[i].Weights); diff != "" {
			t.Fatalf("weights (-want +got):\n%s", diff)
		}
	}
}

func TestEncoderWeightAndSourceLimits(t *testing.T) {
	for _, tc := range []struct {
		name     string
		weight   int64
		line     int64
		errText  string
		addExtra bool
	}{
		{
			name:   "maximum exact values",
			weight: maxExactInteger,
			line:   maxExactInteger,
		},
		{
			name:    "negative weight",
			weight:  -1,
			errText: "weight -1 is outside",
		},
		{
			name:    "inexact weight",
			weight:  maxExactInteger + 1,
			errText: "is outside [0, 2^53-1]",
		},
		{
			name:     "inexact group total",
			weight:   maxExactInteger,
			errText:  "total weight exceeds",
			addExtra: true,
		},
		{
			name:    "negative source line",
			weight:  1,
			line:    -1,
			errText: "invalid source line -1",
		},
		{
			name:    "inexact source line",
			weight:  1,
			line:    maxExactInteger + 1,
			errText: "invalid source line",
		},
		{
			name: "zero weight omits source",
			line: -1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := encoder{unit: "bytes"}
			w := testWindow("bytes", tc.weight)
			w.ContainerID = "container"
			w.Aggregated.Frames[0].Line = tc.line
			if tc.addExtra {
				w.Aggregated.Samples = append(w.Aggregated.Samples, types.AggregatedSample{
					KernelStack: 1,
					Value:       1,
				})
			}
			err := e.appendProfiles(t.Context(), w)
			if tc.errText == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid sample accepted")
			}
			for _, part := range []string{tc.errText, `container "container"`, `profile "` + w.ProfileType + `"`, "label set 0"} {
				if !strings.Contains(err.Error(), part) {
					t.Fatalf("error %q lacks %q", err, part)
				}
			}
		})
	}
}

func TestEncoderUnknownStack(t *testing.T) {
	e := encoder{unit: "bytes"}
	w := testWindow("bytes", 5)
	w.Aggregated.Frames = nil
	w.Aggregated.Stacks = nil
	w.Aggregated.Samples[0].UserStack = 0
	if err := e.appendProfiles(t.Context(), w); err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	if err := e.write(t.Context(), &data); err != nil {
		t.Fatal(err)
	}
	var got speedscopeFile
	if err := json.Unmarshal(data.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Shared.Frames) != 1 || got.Shared.Frames[0].Name != "[unknown]" || len(got.Profiles) != 1 {
		t.Fatalf("unknown stack lost: %+v", got)
	}
	if diff := cmp.Diff([][]int{{0}}, got.Profiles[0].Samples); diff != "" {
		t.Fatalf("samples (-want +got):\n%s", diff)
	}
}

func TestEncoderWriteCancellation(t *testing.T) {
	for _, tc := range []struct {
		name          string
		cancelBefore  bool
		writeErr      error
		expectedError error
	}{
		{
			name:          "before encoding",
			cancelBefore:  true,
			expectedError: context.Canceled,
		},
		{
			name:          "during write",
			expectedError: context.Canceled,
		},
		{
			name:          "write failure during cancellation",
			writeErr:      io.ErrClosedPipe,
			expectedError: io.ErrClosedPipe,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancelBefore {
				cancel()
			}
			written := false
			writer := writeFunc(func(data []byte) (int, error) {
				written = true
				cancel()
				return len(data), tc.writeErr
			})
			e := encoder{}
			if err := e.write(ctx, writer); !errors.Is(err, tc.expectedError) {
				t.Fatalf("write error = %v, want %v", err, tc.expectedError)
			}
			if written == tc.cancelBefore {
				t.Fatalf("written = %t, canceled before encoding = %t", written, tc.cancelBefore)
			}
		})
	}
}
