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

package aggregator

import (
	"bytes"
	"testing"

	ptree "github.com/grafana/pyroscope/pkg/og/storage/tree"
)

func TestConvertProfilePreservesCollectionMetadata(t *testing.T) {
	for _, tc := range []struct {
		name     string
		duration int64
	}{
		{name: "partial window", duration: 2375000123},
		{name: "snapshot without duration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := &ptree.Profile{
				TimeNanos:     1788912345123456789,
				DurationNanos: tc.duration,
				Period:        10000000,
				PeriodType:    &ptree.ValueType{Type: 1, Unit: 2},
				SampleType:    []*ptree.ValueType{{Type: 1, Unit: 2}},
				StringTable:   []string{"", "cpu", "nanoseconds", "hot"},
				Function:      []*ptree.Function{{Id: 1, Name: 3}},
				Location:      []*ptree.Location{{Id: 1, Line: []*ptree.Line{{FunctionId: 1}}}},
				Sample:        []*ptree.Sample{{LocationId: []uint64{1}, Value: []int64{30000000}}},
			}
			converted, err := convertProfile(profile)
			if err != nil {
				t.Fatalf("convertProfile(): %v", err)
			}
			if converted.TimeNanos != profile.TimeNanos || converted.DurationNanos != profile.DurationNanos {
				t.Fatalf("converted window = (%d, %d), want (%d, %d)",
					converted.TimeNanos, converted.DurationNanos, profile.TimeNanos, profile.DurationNanos)
			}
			before, err := profile.MarshalVT()
			if err != nil {
				t.Fatal(err)
			}
			after, err := converted.MarshalVT()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("conversion changed profile metadata or samples")
			}
		})
	}
}
