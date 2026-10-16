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
	"math"
	"testing"

	"github.com/ccfos/huatuo/internal/profiler/output"
)

func decodeSpeedscope(t *testing.T, formatter *Formatter) speedscopeFile {
	t.Helper()

	var buf bytes.Buffer
	if err := formatter.Write(&buf); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	var got speedscopeFile
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal speedscope output: %v", err)
	}
	return got
}

func TestNewDefaultsTo100Hz(t *testing.T) {
	formatter := New(0)
	if formatter.sampleDuration != 0.01 {
		t.Fatalf("sampleDuration = %v, want 0.01", formatter.sampleDuration)
	}
}

func TestFormatterWritesSampledProfile(t *testing.T) {
	formatter := New(100)
	if err := formatter.Add(&output.Sample{
		Frames:       []string{"root", "worker"},
		FrameDetails: []output.Frame{{File: "root.c", Line: 1}, {File: "worker.c", Line: 42}},
		Count:        3,
		ThreadID:     "t1",
		ThreadName:   "worker",
		PID:          1234,
	}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	got := decodeSpeedscope(t, formatter)
	if got.Schema != "https://www.speedscope.app/file-format-schema.json" {
		t.Errorf("schema = %q", got.Schema)
	}
	if got.Exporter != "huatuo-profiler" {
		t.Errorf("exporter = %q", got.Exporter)
	}
	if len(got.Shared.Frames) != 2 {
		t.Fatalf("shared frames = %d, want 2", len(got.Shared.Frames))
	}
	if frame := got.Shared.Frames[1]; frame.Name != "worker" || frame.File != "worker.c" || frame.Line != 42 {
		t.Errorf("frame[1] = %+v, want worker with file/line metadata", frame)
	}
	if len(got.Profiles) != 1 {
		t.Fatalf("profiles = %d, want 1", len(got.Profiles))
	}
	profile := got.Profiles[0]
	if profile.Type != "sampled" || profile.Unit != "seconds" || profile.Name != "worker" {
		t.Errorf("profile = %+v", profile)
	}
	if len(profile.Samples) != 3 {
		t.Fatalf("samples = %d, want 3 (Count expansion)", len(profile.Samples))
	}
	if len(profile.Weights) != 3 || math.Abs(profile.Weights[0]-0.01) > 1e-9 {
		t.Errorf("weights = %v, want 0.01 each", profile.Weights)
	}
	if math.Abs(profile.EndValue-0.03) > 1e-9 {
		t.Errorf("endValue = %v, want 0.03", profile.EndValue)
	}
	if got.ActiveProfileIdx != 0 {
		t.Errorf("activeProfileIndex = %d, want 0", got.ActiveProfileIdx)
	}
}

func TestFormatterDeduplicatesFramesAcrossThreads(t *testing.T) {
	formatter := New(100)
	shared := []string{"root", "hot"}
	if err := formatter.Add(&output.Sample{Frames: shared, Count: 2, ThreadID: "a", ThreadName: "A"}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if err := formatter.Add(&output.Sample{Frames: shared, Count: 1, ThreadID: "b", ThreadName: "B"}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	got := decodeSpeedscope(t, formatter)
	if len(got.Shared.Frames) != 2 {
		t.Fatalf("shared frames = %d, want 2 (deduplicated)", len(got.Shared.Frames))
	}
	if len(got.Profiles) != 2 {
		t.Fatalf("profiles = %d, want 2", len(got.Profiles))
	}
	if got.Profiles[0].Name != "A" || got.Profiles[1].Name != "B" {
		t.Errorf("profile order = [%q %q], want [A B]", got.Profiles[0].Name, got.Profiles[1].Name)
	}
	if len(got.Profiles[0].Samples) != 2 || len(got.Profiles[1].Samples) != 1 {
		t.Errorf("sample counts = [%d %d], want [2 1]", len(got.Profiles[0].Samples), len(got.Profiles[1].Samples))
	}
	if got.ActiveProfileIdx != 0 {
		t.Errorf("activeProfileIndex = %d, want 0", got.ActiveProfileIdx)
	}
}

func TestFormatterSkipsFrameLessSamplesAndResets(t *testing.T) {
	formatter := New(100)
	if err := formatter.Add(&output.Sample{Count: 5, ThreadID: "t"}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if !formatter.IsEmpty() {
		t.Fatal("frame-less sample must not populate the formatter")
	}

	if err := formatter.Add(&output.Sample{Frames: []string{"root"}, Count: 1, ThreadID: "t"}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if formatter.IsEmpty() {
		t.Fatal("formatter with a stack must not be empty")
	}

	formatter.Reset()
	if !formatter.IsEmpty() {
		t.Fatal("Reset() must clear accumulated state")
	}
	if formatter.frameList != nil || len(formatter.threads) != 0 {
		t.Fatal("Reset() must drop frames and threads")
	}
}

func TestFormatterTreatsNonPositiveCountAsOne(t *testing.T) {
	formatter := New(100)
	if err := formatter.Add(&output.Sample{Frames: []string{"root"}, Count: 0, ThreadID: "t"}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	got := decodeSpeedscope(t, formatter)
	if len(got.Profiles) != 1 {
		t.Fatalf("profiles = %d, want 1", len(got.Profiles))
	}
	if len(got.Profiles[0].Samples) != 1 {
		t.Fatalf("samples = %d, want 1 for non-positive Count", len(got.Profiles[0].Samples))
	}
}
