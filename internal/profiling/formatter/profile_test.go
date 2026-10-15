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
	"slices"
	"testing"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
)

func TestWalkProfileRejectsInvalidReferences(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile *profilev1.Profile
	}{
		{name: "nil profile"},
		{name: "zero function id", profile: &profilev1.Profile{Function: []*profilev1.Function{{}}}},
		{name: "duplicate function", profile: &profilev1.Profile{Function: []*profilev1.Function{{Id: 1}, {Id: 1}}}},
		{name: "zero location id", profile: &profilev1.Profile{Location: []*profilev1.Location{{}}}},
		{name: "duplicate location", profile: &profilev1.Profile{Location: []*profilev1.Location{{Id: 1}, {Id: 1}}}},
		{name: "nil function", profile: &profilev1.Profile{Function: []*profilev1.Function{nil}}},
		{name: "nil location", profile: &profilev1.Profile{Location: []*profilev1.Location{nil}}},
		{name: "nil sample", profile: &profilev1.Profile{Sample: []*profilev1.Sample{nil}}},
		{name: "missing value", profile: &profilev1.Profile{Sample: []*profilev1.Sample{{}}}},
		{name: "missing location", profile: &profilev1.Profile{Sample: []*profilev1.Sample{{Value: []int64{1}, LocationId: []uint64{1}}}}},
		{name: "missing function", profile: &profilev1.Profile{
			Sample:   []*profilev1.Sample{{Value: []int64{1}, LocationId: []uint64{1}}},
			Location: []*profilev1.Location{{Id: 1, Line: []*profilev1.Line{{FunctionId: 1}}}},
		}},
		{name: "invalid name index", profile: &profilev1.Profile{
			Sample:   []*profilev1.Sample{{Value: []int64{1}, LocationId: []uint64{1}}},
			Location: []*profilev1.Location{{Id: 1, Line: []*profilev1.Line{{FunctionId: 1}}}},
			Function: []*profilev1.Function{{Id: 1, Name: -1}},
		}},
		{name: "nil line", profile: &profilev1.Profile{
			Sample:   []*profilev1.Sample{{Value: []int64{1}, LocationId: []uint64{1}}},
			Location: []*profilev1.Location{{Id: 1, Line: []*profilev1.Line{nil}}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := WalkProfile(t.Context(), tc.profile, func([]string, int64) error {
				t.Fatal("invalid sample reached visitor")
				return nil
			}); err == nil {
				t.Fatal("invalid profile accepted")
			}
		})
	}
}

func TestProfileReaderPreservesInlineFramesAndAddresses(t *testing.T) {
	profile := &profilev1.Profile{
		StringTable: []string{"", "leaf", "caller", "app.go"},
		Function:    []*profilev1.Function{{Id: 1, Name: 1, Filename: 3}, {Id: 2, Name: 2, Filename: 3}},
		Location: []*profilev1.Location{
			{Id: 1, Line: []*profilev1.Line{{FunctionId: 1, Line: 10}, {FunctionId: 2, Line: 20}}},
			{Id: 2, Address: 0x1234},
		},
		Sample: []*profilev1.Sample{{LocationId: []uint64{1, 2}, Value: []int64{7}}},
	}
	reader, err := NewProfileReader(profile)
	if err != nil {
		t.Fatal(err)
	}
	type frame struct {
		name, file string
		line       int64
	}
	var got []frame
	if err := reader.WalkStack(profile.Sample[0], func(name, file string, line int64) error {
		got = append(got, frame{name: name, file: file, line: line})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []frame{{name: "0x1234"}, {name: "caller", file: "app.go", line: 20}, {name: "leaf", file: "app.go", line: 10}}
	if !slices.EqualFunc(got, want, func(a, b frame) bool {
		return a.name == b.name && a.file == b.file && a.line == b.line
	}) {
		t.Fatalf("frames = %+v, want %+v", got, want)
	}
	if err := WalkProfile(t.Context(), profile, func(names []string, weight int64) error {
		if !slices.Equal(names, []string{"0x1234", "caller", "leaf"}) || weight != 7 {
			t.Fatalf("stack = %v, weight = %d", names, weight)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
