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

	"huatuo-bamai/internal/profiler/output"
)

// Batch samples with huge counts come from collapsed files, which may be
// produced by the profiled tenant container. The formatter must stay
// memory-bounded per sample instead of expanding Count entries.
func TestAddHugeCountStaysBounded(t *testing.T) {
	f := New(100) // 10ms per sample

	const hugeCount = int64(1_000_000_000)
	if err := f.Add(&output.Sample{Frames: []string{"a", "b"}, Count: hugeCount}); err != nil {
		t.Fatalf("Add() error=%v", err)
	}

	ts := f.threads["default"]
	if len(ts.samples) != 1 {
		t.Fatalf("samples=%d, want 1 weighted entry", len(ts.samples))
	}
	if got := ts.weights[0]; got != float64(hugeCount)*f.sampleDuration {
		t.Errorf("weight=%v, want %v", got, float64(hugeCount)*f.sampleDuration)
	}
	if ts.total != hugeCount {
		t.Errorf("total=%d, want %d", ts.total, hugeCount)
	}

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("Write() error=%v", err)
	}

	var file struct {
		Profiles []struct {
			EndValue float64   `json:"endValue"`
			Samples  [][]int   `json:"samples"`
			Weights  []float64 `json:"weights"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(buf.Bytes(), &file); err != nil {
		t.Fatalf("output is not valid speedscope JSON: %v", err)
	}
	if len(file.Profiles) != 1 {
		t.Fatalf("profiles=%d, want 1", len(file.Profiles))
	}
	profile := file.Profiles[0]
	if want := float64(hugeCount) * f.sampleDuration; profile.EndValue != want {
		t.Errorf("endValue=%v, want %v", profile.EndValue, want)
	}
	if len(profile.Samples) != 1 || len(profile.Weights) != 1 {
		t.Errorf("samples=%d weights=%d, want 1/1", len(profile.Samples), len(profile.Weights))
	}
}

func TestAddSingleSampleDefaultWeight(t *testing.T) {
	f := New(100)

	if err := f.Add(&output.Sample{Frames: []string{"a"}}); err != nil {
		t.Fatalf("Add() error=%v", err)
	}

	ts := f.threads["default"]
	if len(ts.samples) != 1 || len(ts.weights) != 1 {
		t.Fatalf("samples=%d weights=%d, want 1/1", len(ts.samples), len(ts.weights))
	}
	if got := ts.weights[0]; got != f.sampleDuration {
		t.Errorf("weight=%v, want %v", got, f.sampleDuration)
	}
}
