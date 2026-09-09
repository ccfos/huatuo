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

package profiler

import (
	"testing"
	"time"

	pprof "github.com/google/pprof/profile"
)

func TestParseTreeCollectionMetadataRoundTrip(t *testing.T) {
	start := time.Unix(1_700_000_000, 123)
	duration := 5*time.Second + 17*time.Nanosecond
	tests := []struct {
		name     string
		typ      string
		option   *ParseOption
		want     int64
		period   int64
		duration time.Duration
	}{
		{name: "CPU sample count scales once", typ: ProfileTypeCpuSample, option: &ParseOption{SampleRate: 100, Duration: duration}, want: 70_000_000, period: 10_000_000, duration: duration},
		{name: "off-CPU nanoseconds", typ: ProfileTypeOffCpuSample, option: &ParseOption{Duration: duration}, want: 7, period: 1, duration: duration},
		{name: "memory bytes", typ: ProfileTypeMemSample, option: &ParseOption{Duration: duration}, want: 7, period: 1, duration: duration},
		{name: "explicit snapshot", typ: ProfileTypeMemSample, option: &ParseOption{}, want: 7, period: 1},
		{name: "legacy nil option", typ: ProfileTypeMemSample, want: 7, period: 1},
	}
	for index := range tests {
		tt := &tests[index]
		t.Run(tt.name, func(t *testing.T) {
			item := &TreeItem{Stack: [][]byte{[]byte("main"), []byte("work")}, Value: 7}
			for range 2 {
				data, err := ParseTree(start, tt.typ, []*TreeItem{item}, tt.option)
				if err != nil {
					t.Fatal(err)
				}
				wire, err := data.Profile.MarshalVT()
				if err != nil {
					t.Fatal(err)
				}
				decoded, err := pprof.ParseData(wire)
				if err != nil {
					t.Fatalf("parse serialized pprof: %v", err)
				}
				if decoded.TimeNanos != start.UnixNano() || decoded.DurationNanos != tt.duration.Nanoseconds() {
					t.Fatalf("time/duration = %d/%d, want %d/%d", decoded.TimeNanos, decoded.DurationNanos, start.UnixNano(), tt.duration.Nanoseconds())
				}
				if decoded.Period != tt.period || len(decoded.Sample) != 1 || decoded.Sample[0].Value[0] != tt.want {
					t.Fatalf("period/samples = %d/%v, want %d/[%d]", decoded.Period, decoded.Sample, tt.period, tt.want)
				}
			}
			if item.Value != 7 {
				t.Fatalf("input value changed to %d", item.Value)
			}
		})
	}
}

func TestParseTreeRejectsNegativeDuration(t *testing.T) {
	if _, err := ParseTree(time.Unix(1_700_000_000, 0), ProfileTypeCpuSample, nil, &ParseOption{Duration: -time.Nanosecond}); err == nil {
		t.Fatal("ParseTree accepted a negative duration")
	}
}
