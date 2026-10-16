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

package chrometrace

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/ccfos/huatuo/internal/profiler/output"
)

func TestFormatterSeparatesEqualThreadIDsAcrossProcesses(t *testing.T) {
	formatter := New(100)
	for _, sample := range []*output.Sample{
		{PID: 101, ThreadID: "7", Frames: []string{"first"}, Timestamp: 1},
		{PID: 202, ThreadID: "7", Frames: []string{"second"}, Timestamp: 2},
	} {
		if err := formatter.Add(sample); err != nil {
			t.Fatalf("Add() error = %v", err)
		}
	}

	var buf bytes.Buffer
	if err := formatter.Write(&buf); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	var trace traceOutput
	if err := json.Unmarshal(buf.Bytes(), &trace); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	var metadata, begins int
	for _, event := range trace.TraceEvents {
		switch event.Ph {
		case "M":
			metadata++
		case "B":
			begins++
		case "E":
			if event.TS == 2 {
				t.Fatalf("second process closed the first process stack: %+v", event)
			}
		}
	}
	if metadata != 2 {
		t.Fatalf("thread metadata events = %d, want 2", metadata)
	}
	if begins != 2 {
		t.Fatalf("begin events = %d, want 2", begins)
	}
}
