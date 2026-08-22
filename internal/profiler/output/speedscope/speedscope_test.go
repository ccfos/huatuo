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

func TestFormatterWritesThreadProfileWithFrameDetails(t *testing.T) {
	f := New(50)
	if err := f.Add(&output.Sample{
		ThreadID:   "7",
		ThreadName: "worker",
		Count:      2,
		Frames:     []string{"main", "work"},
		FrameDetails: []output.Frame{
			{File: "main.go", Line: 10},
			{File: "worker.go", Line: 20},
		},
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := f.Add(&output.Sample{}); err != nil {
		t.Fatalf("Add empty sample: %v", err)
	}

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("Write: %v", err)
	}
	var file speedscopeFile
	if err := json.Unmarshal(buf.Bytes(), &file); err != nil {
		t.Fatalf("decode speedscope JSON: %v", err)
	}
	if len(file.Shared.Frames) != 2 || file.Shared.Frames[1].File != "worker.go" || file.Shared.Frames[1].Line != 20 {
		t.Fatalf("frames = %#v, want source-aware frames", file.Shared.Frames)
	}
	if len(file.Profiles) != 1 || file.Profiles[0].Name != "worker" || len(file.Profiles[0].Samples) != 2 {
		t.Fatalf("profiles = %#v, want one worker profile with two samples", file.Profiles)
	}
	if file.Profiles[0].EndValue != 0.04 {
		t.Errorf("EndValue = %v, want 0.04", file.Profiles[0].EndValue)
	}
}

func TestFormatterResetClearsSamples(t *testing.T) {
	f := New(100)
	_ = f.Add(&output.Sample{Frames: []string{"main"}})
	f.Reset()
	if !f.IsEmpty() {
		t.Fatal("Reset left formatter non-empty")
	}
}
