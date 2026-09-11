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

// eventSummary is the (phase, name) pair each assertion works with.
type eventSummary struct {
	Ph   string
	Name string
}

func summarize(events []event) []eventSummary {
	got := make([]eventSummary, 0, len(events))
	for _, e := range events {
		got = append(got, eventSummary{Ph: e.Ph, Name: e.Name})
	}
	return got
}

func addSample(t *testing.T, f *Formatter, s *output.Sample) {
	t.Helper()
	if err := f.Add(s); err != nil {
		t.Fatalf("Add(%+v): %v", s, err)
	}
}

// streamingSamples is a realistic outermost-first stack evolution on one
// thread: main calls a, a calls b, then both return.
func streamingSamples() []*output.Sample {
	return []*output.Sample{
		{PID: 42, ThreadID: "7", ThreadName: "worker", Timestamp: 100, Count: 1, Frames: []string{"main"}},
		{PID: 42, ThreadID: "7", Timestamp: 200, Count: 1, Frames: []string{"main", "a"}},
		{PID: 42, ThreadID: "7", Timestamp: 300, Count: 1, Frames: []string{"main", "a", "b"}},
		{PID: 42, ThreadID: "7", Timestamp: 400, Count: 1, Frames: []string{"main"}},
	}
}

func TestFormatterAddStreamsNestedEvents(t *testing.T) {
	f := New(100)
	for _, s := range streamingSamples() {
		addSample(t, f, s)
	}

	// "B"/"E" nest by emission order, so outermost frames must open first
	// and innermost frames must close first: main{a{b}}. The final sample
	// returns to "main", which stays open until Write.
	want := []eventSummary{
		{"M", "thread_name"},
		{"B", "main"},
		{"B", "a"},
		{"B", "b"},
		{"E", "b"},
		{"E", "a"},
	}
	if got := summarize(f.events); !equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestFormatterAddReplacesLeafFrame(t *testing.T) {
	f := New(100)
	addSample(t, f, &output.Sample{PID: 1, ThreadID: "t", Timestamp: 100, Count: 1, Frames: []string{"main", "a"}})
	addSample(t, f, &output.Sample{PID: 1, ThreadID: "t", Timestamp: 200, Count: 1, Frames: []string{"main", "b"}})

	// The shared outermost prefix "main" must stay open; only the leaf
	// changes.
	want := []eventSummary{
		{"M", "thread_name"},
		{"B", "main"},
		{"B", "a"},
		{"E", "a"},
		{"B", "b"},
	}
	if got := summarize(f.events); !equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestFormatterAddTracksThreadsSeparately(t *testing.T) {
	f := New(100)
	addSample(t, f, &output.Sample{PID: 1, ThreadID: "t1", ThreadName: "worker", Timestamp: 100, Count: 1, Frames: []string{"main", "a"}})
	addSample(t, f, &output.Sample{PID: 1, ThreadID: "t2", Timestamp: 200, Count: 1, Frames: []string{"x"}})

	// The unnamed thread falls back to its ID for the metadata name, and
	// its stack is diffed independently of t1's.
	want := []eventSummary{
		{"M", "thread_name"},
		{"B", "main"},
		{"B", "a"},
		{"M", "thread_name"},
		{"B", "x"},
	}
	if got := summarize(f.events); !equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for _, e := range f.events {
		if e.Ph != "M" {
			continue
		}
		if e.TID == "t1" && e.Args["name"] != "worker" {
			t.Fatalf("t1 metadata name = %v, want worker", e.Args["name"])
		}
		if e.TID == "t2" && e.Args["name"] != "t2" {
			t.Fatalf("t2 metadata name = %v, want t2", e.Args["name"])
		}
	}
}

func TestFormatterWriteClosesOpenStack(t *testing.T) {
	f := New(100) // 10,000 µs per synthesized sample
	addSample(t, f, &output.Sample{PID: 42, ThreadID: "7", Timestamp: 100, Count: 1, Frames: []string{"main", "a"}})
	addSample(t, f, &output.Sample{PID: 42, ThreadID: "7", Timestamp: 200, Count: 1, Frames: []string{"main", "a"}})

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var trace traceOutput
	if err := json.Unmarshal(buf.Bytes(), &trace); err != nil {
		t.Fatalf("decode trace: %v", err)
	}
	events := trace.TraceEvents
	if len(events) != 5 {
		t.Fatalf("event count = %d, want 5: %+v", len(events), events)
	}

	// The final flush must close the open stack innermost-first. With two
	// samples consumed, the synthesized final timestamp is 2 * 10,000 µs.
	want := []eventSummary{{"M", "thread_name"}, {"B", "main"}, {"B", "a"}, {"E", "a"}, {"E", "main"}}
	if got := summarize(events); !equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for _, e := range events[3:] {
		if e.TS != 20000 {
			t.Fatalf("final event %q ts = %v, want 20000", e.Name, e.TS)
		}
	}
}

func TestFormatterAddBatchNestsXEvents(t *testing.T) {
	f := New(100)
	addSample(t, f, &output.Sample{PID: 7, Count: 10, Frames: []string{"root", "child", "leaf"}})

	if len(f.events) != 3 {
		t.Fatalf("event count = %d, want 3", len(f.events))
	}

	// Duration is Count / sampleRate: 10 * 10,000 µs = 100,000 µs. The
	// outermost frame opens earliest and lives longest; each nested frame
	// must sit strictly inside its parent. A missing ThreadID falls back
	// to "main".
	var prev event
	for i, e := range f.events {
		if e.Ph != "X" || e.TID != "main" {
			t.Fatalf("event %d = %+v, want X event on thread main", i, e)
		}
		if i > 0 {
			if e.TS <= prev.TS || e.TS+e.Dur > prev.TS+prev.Dur {
				t.Fatalf("event %d (%v..%v] not nested in (%v..%v]", i, e.TS, e.TS+e.Dur, prev.TS, prev.TS+prev.Dur)
			}
		}
		prev = e
	}
	if got := f.events[0].Name; got != "root" {
		t.Fatalf("outermost X event name = %q, want root", got)
	}
	if got := f.events[0].TS; got != 0 {
		t.Fatalf("outermost X event ts = %v, want 0", got)
	}
	if got := f.events[0].Dur; got != 100000 {
		t.Fatalf("outermost X event dur = %v, want 100000", got)
	}
}

func TestFormatterAddCounterTags(t *testing.T) {
	f := New(0) // exercises the default sample rate too
	addSample(t, f, &output.Sample{PID: 1, ThreadID: "c", ThreadName: "runtime", Tags: map[string]string{
		"requests": "12",
		"state":    "ready",
	}})
	addSample(t, f, &output.Sample{PID: 1, ThreadID: "c"}) // no frames, no tags

	if len(f.events) != 2 {
		t.Fatalf("event count = %d, want 2 (metadata + counter)", len(f.events))
	}

	counter := f.events[1]
	if counter.Ph != "C" || counter.Name != "runtime" {
		t.Fatalf("counter event = %+v, want C event named runtime", counter)
	}
	if got := counter.Args["requests"]; got != float64(12) {
		t.Fatalf("numeric tag = %#v, want float64(12)", got)
	}
	if got := counter.Args["state"]; got != "ready" {
		t.Fatalf("non-numeric tag = %#v, want \"ready\"", got)
	}
}

func TestFormatterNewDefaultsSampleRate(t *testing.T) {
	tests := []struct {
		name  string
		rate  float64
		want  float64
		isNaN bool
	}{
		{name: "zero defaults to 100 Hz", rate: 0, want: 100},
		{name: "negative defaults to 100 Hz", rate: -1, want: 100},
		{name: "positive rate is kept", rate: 250, want: 250},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := New(tt.rate)
			if f.sampleRateHz != tt.want {
				t.Fatalf("sampleRateHz = %v, want %v", f.sampleRateHz, tt.want)
			}
		})
	}
	if got := New(0).Name(); got != "chrometrace" {
		t.Fatalf("Name() = %q, want chrometrace", got)
	}
}

func TestFormatterReset(t *testing.T) {
	f := New(100)
	addSample(t, f, &output.Sample{PID: 1, ThreadID: "t", Timestamp: 100, Count: 1, Frames: []string{"main"}})
	if f.IsEmpty() {
		t.Fatal("formatter empty after Add")
	}

	f.Reset()
	if !f.IsEmpty() {
		t.Fatalf("events retained after Reset: %+v", f.events)
	}

	// After Reset the thread is unknown again and the synthesized
	// timestamp restarts from sample 0.
	addSample(t, f, &output.Sample{PID: 1, ThreadID: "t", Count: 1, Frames: []string{"main"}})
	want := []eventSummary{{"M", "thread_name"}, {"B", "main"}}
	if got := summarize(f.events); !equal(got, want) {
		t.Fatalf("events after Reset = %v, want %v", got, want)
	}
	if ts := f.events[1].TS; ts != 0 {
		t.Fatalf("ts after Reset = %v, want 0", ts)
	}
}

func TestFormatterTimestampFor(t *testing.T) {
	f := New(250)
	if got := f.timestampFor(&output.Sample{Timestamp: 1234}); got != 1234 {
		t.Fatalf("explicit timestamp = %v, want 1234", got)
	}
	// Synthesized stamps advance by 1 / sampleRate seconds per sample.
	f.sampleIdx = 2
	if got := f.timestampFor(&output.Sample{}); got != 8000 {
		t.Fatalf("synthesized timestamp = %v, want 8000", got)
	}
}

func equal(a, b []eventSummary) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
