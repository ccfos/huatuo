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
	"reflect"
	"testing"

	"github.com/ccfos/huatuo/internal/profiler/output"
)

func decodeTrace(t *testing.T, formatter *Formatter) traceOutput {
	t.Helper()

	var buf bytes.Buffer
	if err := formatter.Write(&buf); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	var got traceOutput
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal trace output: %v", err)
	}
	return got
}

func TestFormatterStreamingUnchangedStackEmitsNoNewEvents(t *testing.T) {
	formatter := New(100)
	sample := &output.Sample{
		Frames:     []string{"root", "hot"},
		Count:      1,
		ThreadID:   "t1",
		ThreadName: "worker",
		PID:        7,
	}
	if err := formatter.Add(sample); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	// One thread-name event, a begin event per frame, and the closing events
	// flushed by Write.
	after := decodeTrace(t, formatter)
	if len(after.TraceEvents) != 5 {
		t.Fatalf("events = %d, want 5", len(after.TraceEvents))
	}

	if err := formatter.Add(sample); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	repeated := decodeTrace(t, formatter)
	if len(repeated.TraceEvents) != 5 {
		t.Fatalf("repeating the same stack emitted %d events, want 5", len(repeated.TraceEvents))
	}
}

func TestFormatterStreamingDiffsConsecutiveStacks(t *testing.T) {
	formatter := New(100)
	if err := formatter.Add(&output.Sample{
		Frames:     []string{"root", "before"},
		Count:      1,
		ThreadID:   "t1",
		ThreadName: "worker",
		PID:        7,
	}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if err := formatter.Add(&output.Sample{
		Frames:     []string{"root", "after"},
		Count:      1,
		ThreadID:   "t1",
		ThreadName: "worker",
		PID:        7,
	}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	got := decodeTrace(t, formatter)

	var phases []string
	for _, event := range got.TraceEvents {
		phases = append(phases, event.Ph+":"+event.Name)
	}
	want := []string{
		"M:thread_name",
		"B:before",
		"B:root",
		"E:root",
		"E:before",
		"B:after",
		"B:root",
		"E:root",
		"E:after",
	}
	if !reflect.DeepEqual(phases, want) {
		t.Fatalf("event phases = %v, want %v", phases, want)
	}
}

func TestFormatterBatchEmitsCompleteEvents(t *testing.T) {
	formatter := New(100)
	if err := formatter.Add(&output.Sample{
		Frames: []string{"root", "leaf"},
		Count:  4,
		PID:    9,
	}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	got := decodeTrace(t, formatter)
	if len(got.TraceEvents) != 2 {
		t.Fatalf("events = %d, want 2", len(got.TraceEvents))
	}
	root, leaf := got.TraceEvents[0], got.TraceEvents[1]
	if root.Ph != "X" || root.Name != "root" || root.TID != "main" {
		t.Errorf("root event = %+v", root)
	}
	if root.Dur != 4*1e6/100 {
		t.Errorf("root duration = %v, want %v", root.Dur, 4*1e6/100)
	}
	if leaf.TS <= root.TS {
		t.Errorf("leaf start = %v, want > %v", leaf.TS, root.TS)
	}
	if leaf.Dur >= root.Dur {
		t.Errorf("leaf duration = %v, want < %v", leaf.Dur, root.Dur)
	}
}

func TestFormatterEmitsCounterEventsFromTags(t *testing.T) {
	formatter := New(100)
	if err := formatter.Add(&output.Sample{
		Count:      1,
		ThreadID:   "t2",
		ThreadName: "cpu",
		PID:        3,
		Tags:       map[string]string{"cpu": "12.5", "state": "running"},
	}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	got := decodeTrace(t, formatter)
	if len(got.TraceEvents) != 2 {
		t.Fatalf("events = %d, want a metadata and a counter event", len(got.TraceEvents))
	}
	if got.TraceEvents[0].Ph != "M" {
		t.Errorf("first event = %+v, want thread metadata", got.TraceEvents[0])
	}
	counter := got.TraceEvents[1]
	if counter.Ph != "C" || counter.Name != "cpu" || counter.TID != "t2" {
		t.Errorf("counter event = %+v", counter)
	}
	if value, ok := counter.Args["cpu"].(float64); !ok || value != 12.5 {
		t.Errorf("numeric tag = %#v, want float64(12.5)", counter.Args["cpu"])
	}
	if value, ok := counter.Args["state"].(string); !ok || value != "running" {
		t.Errorf("string tag = %#v, want %q", counter.Args["state"], "running")
	}
}

func TestFormatterIgnoresFrameLessSamplesWithoutTags(t *testing.T) {
	formatter := New(0)
	if err := formatter.Add(&output.Sample{Count: 1}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if !formatter.IsEmpty() {
		t.Fatal("frame-less sample without tags must not produce output")
	}

	if err := formatter.Add(&output.Sample{Frames: []string{"root"}, Count: 1}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if formatter.IsEmpty() {
		t.Fatal("formatter with frames must not be empty")
	}

	formatter.Reset()
	if !formatter.IsEmpty() {
		t.Fatal("Reset() must clear accumulated events")
	}
}
