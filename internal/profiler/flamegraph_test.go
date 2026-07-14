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

	"huatuo-bamai/internal/flamegraph"
)

func TestParseFlamegraphFrames(t *testing.T) {
	start := time.Unix(1700000000, 0).UTC()
	data, err := ParseFlamegraphFrames(start, ProfileTypeCpuSample, []flamegraph.FrameData{
		{Level: 0, Value: 5, Label: "root"},
		{Level: 1, Value: 3, Self: 3, Label: "left"},
		{Level: 1, Value: 2, Self: 2, Label: "right"},
	}, &ParseOption{SampleRate: 100})
	if err != nil {
		t.Fatalf("ParseFlamegraphFrames returned error: %v", err)
	}
	if data.Profile.TimeNanos != start.UnixNano() {
		t.Errorf("TimeNanos = %d, want %d", data.Profile.TimeNanos, start.UnixNano())
	}
	if len(data.Profile.Sample) != 2 {
		t.Fatalf("samples = %d, want 2", len(data.Profile.Sample))
	}
	if data.Profile.Period != int64(10*time.Millisecond) {
		t.Errorf("period = %d, want %d", data.Profile.Period, 10*time.Millisecond)
	}
}

func TestParseFlamegraphFramesRejectsInvalidInput(t *testing.T) {
	for _, frames := range [][]flamegraph.FrameData{
		{{Level: 1, Self: 1, Label: "bad"}},
		{{Level: 0, Label: "root"}},
	} {
		if _, err := ParseFlamegraphFrames(time.Now(), ProfileTypeCpuSample, frames, nil); err == nil {
			t.Fatalf("ParseFlamegraphFrames(%v) error = nil", frames)
		}
	}
}
