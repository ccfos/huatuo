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

package profile

import (
	"testing"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
	"google.golang.org/protobuf/proto"
)

func TestNewWindowOwnsProfileAndPreservesMetadata(t *testing.T) {
	original := &profilev1.Profile{
		StringTable: []string{"", "cpu", "nanoseconds", "work", "thread", "worker", "bytes"},
		SampleType:  []*profilev1.ValueType{{Type: 1, Unit: 2}},
		PeriodType:  &profilev1.ValueType{Type: 1, Unit: 2}, Period: 10000000,
		TimeNanos: 123, DurationNanos: 456,
		Mapping:  []*profilev1.Mapping{{Id: 1, Filename: 3}},
		Function: []*profilev1.Function{{Id: 1, Name: 3, SystemName: 3}},
		Location: []*profilev1.Location{{Id: 1, MappingId: 1, Line: []*profilev1.Line{{FunctionId: 1, Line: 7}}}},
		Sample: []*profilev1.Sample{{
			LocationId: []uint64{1}, Value: []int64{70000000},
			Label: []*profilev1.Label{{Key: 4, Str: 5}, {Key: 6, Num: 1024, NumUnit: 6}},
		}},
		Comment: []int64{3}, DefaultSampleType: 1,
	}
	data := &Data{ProfileType: ProfileTypeCpuSample, Profile: original.CloneVT()}
	first := NewWindow(data, "container")
	second := NewWindow(data, "container")
	want := original.CloneVT()
	want.TimeNanos, want.DurationNanos = 0, 0
	if first.ContainerID != "container" || first.ProfileType != data.ProfileType || !proto.Equal(first.Profile, want) {
		t.Fatalf("window metadata changed: %v", first)
	}
	first.Profile.StringTable[3] = "mutated"
	first.Profile.Sample[0].Value[0] = 1
	first.Profile.Sample[0].Label[0].Str = 3
	first.Profile.Sample[0].LocationId[0] = 2
	first.Profile.Location[0].Line[0].Line = 9
	first.Profile.Function[0].Name = 0
	first.Profile.Mapping[0].Filename = 0
	first.Profile.SampleType[0].Unit = 0
	first.Profile.PeriodType.Unit = 0
	first.Profile.Comment[0] = 0
	if !proto.Equal(data.Profile, original) || !proto.Equal(second.Profile, want) {
		t.Fatal("window shares mutable profile state with parser data or another snapshot")
	}
}
