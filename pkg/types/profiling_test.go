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

package types

import (
	"encoding/json"
	"testing"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
)

func TestProfilingWindowJSONRoundTrip(t *testing.T) {
	want := &ProfilingWindow{
		ContainerID:              "container-1",
		ProfileType:              "process_cpu:cpu:nanoseconds:cpu:nanoseconds",
		Profile:                  &profilev1.Profile{TimeNanos: 123},
		AggregationOverflowCount: 7,
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var got ProfilingWindow
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got.ContainerID != want.ContainerID || got.ProfileType != want.ProfileType {
		t.Fatalf(
			"profiling window identity = (%q, %q), want (%q, %q)",
			got.ContainerID,
			got.ProfileType,
			want.ContainerID,
			want.ProfileType,
		)
	}
	if got.Profile == nil || got.Profile.TimeNanos != want.Profile.TimeNanos {
		t.Fatalf("profiling window profile time = %v, want %d", got.Profile, want.Profile.TimeNanos)
	}
	if got.AggregationOverflowCount != want.AggregationOverflowCount {
		t.Fatalf(
			"profiling window aggregation overflow count = %d, want %d",
			got.AggregationOverflowCount,
			want.AggregationOverflowCount,
		)
	}
}
