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

package dump

import (
	"bytes"
	"encoding/json"
	"testing"

	"huatuo-bamai/internal/profiler/output"
)

func TestFormatterWritesTextWithSampleMetadata(t *testing.T) {
	f := New(Options{ShowCount: true, Indent: "  "})
	if err := f.Add(&output.Sample{ThreadID: "7", ThreadName: "worker", PID: 42, Count: 3, Frames: []string{"main", "work"}}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("Write: %v", err)
	}
	const want = "Thread 7 (pid=42) \"worker\" [count=3]\n  main\n  work\n\n"
	if got := buf.String(); got != want {
		t.Errorf("text output = %q, want %q", got, want)
	}
}

func TestFormatterWritesJSONAndIgnoresEmptyStacks(t *testing.T) {
	f := New(Options{JSON: true})
	_ = f.Add(&output.Sample{})
	_ = f.Add(&output.Sample{ThreadID: "7", Count: 2, Frames: []string{"main"}})

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("Write: %v", err)
	}
	var entries []dumpEntry
	if err := json.Unmarshal(buf.Bytes(), &entries); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if len(entries) != 1 || entries[0].ThreadID != "7" || entries[0].Count != 2 {
		t.Fatalf("entries = %#v, want one retained sample", entries)
	}
}
