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

	"huatuo-bamai/internal/profiler/output"
)

func TestFormatterStreamingWriteClosesFrames(t *testing.T) {
	f := New(100)
	if err := f.Add(&output.Sample{PID: 42, ThreadID: "7", ThreadName: "worker", Timestamp: 100, Frames: []string{"leaf", "root"}}); err != nil {
		t.Fatalf("Add first sample: %v", err)
	}
	if err := f.Add(&output.Sample{PID: 42, ThreadID: "7", Timestamp: 200, Frames: []string{"root"}}); err != nil {
		t.Fatalf("Add second sample: %v", err)
	}

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var trace traceOutput
	if err := json.Unmarshal(buf.Bytes(), &trace); err != nil {
		t.Fatalf("decode trace: %v", err)
	}

	var metadata, begins, ends int
	for _, ev := range trace.TraceEvents {
		switch ev.Ph {
		case "M":
			metadata++
			if ev.Args["name"] != "worker" {
				t.Fatalf("metadata name = %v, want worker", ev.Args["name"])
			}
		case "B":
			begins++
		case "E":
			ends++
		}
	}
	if metadata != 1 || begins != 2 || ends != 2 {
		t.Fatalf("events metadata=%d begins=%d ends=%d, want 1, 2, 2", metadata, begins, ends)
	}
}

func TestFormatterWritesNumericCounters(t *testing.T) {
	f := New(0)
	if err := f.Add(&output.Sample{ThreadID: "counter", Tags: map[string]string{"requests": "12", "state": "ready"}}); err != nil {
		t.Fatalf("Add counter: %v", err)
	}

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("Write: %v", err)
	}
	var trace traceOutput
	if err := json.Unmarshal(buf.Bytes(), &trace); err != nil {
		t.Fatalf("decode trace: %v", err)
	}
	if len(trace.TraceEvents) != 2 || trace.TraceEvents[1].Ph != "C" {
		t.Fatalf("counter events = %#v, want metadata followed by counter", trace.TraceEvents)
	}
	if got := trace.TraceEvents[1].Args["requests"]; got != float64(12) {
		t.Errorf("numeric tag = %#v, want 12", got)
	}
}
