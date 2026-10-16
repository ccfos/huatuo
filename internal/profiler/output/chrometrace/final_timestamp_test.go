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

func TestWriteClosesStacksAfterExplicitTimestamps(t *testing.T) {
	const start = int64(1_790_000_000_000_000)
	f := New(100)
	for _, sample := range []output.Sample{
		{ThreadID: "a", PID: 42, Count: 1, Frames: []string{"worker"}, Timestamp: start},
		{ThreadID: "b", PID: 42, Count: 1, Frames: []string{"other"}, Timestamp: start + 20_000},
		{ThreadID: "a", PID: 42, Count: 1, Frames: []string{"worker"}, Timestamp: start + 10_000},
	} {
		if err := f.Add(&sample); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		var buf bytes.Buffer
		if err := f.Write(&buf); err != nil {
			t.Fatal(err)
		}
		var trace traceOutput
		if err := json.Unmarshal(buf.Bytes(), &trace); err != nil {
			t.Fatal(err)
		}
		ends := 0
		for _, ev := range trace.TraceEvents {
			if ev.Ph == "E" {
				ends++
				if want := float64(start + 30_000); ev.TS != want {
					t.Errorf("thread %s closed at %v, want %v", ev.TID, ev.TS, want)
				}
			}
		}
		if ends != 2 {
			t.Fatalf("got %d closing events, want 2", ends)
		}
	}
	f.Reset()
	if err := f.Add(&output.Sample{ThreadID: "a", Count: 1, Frames: []string{"fresh"}}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}
	var trace traceOutput
	if err := json.Unmarshal(buf.Bytes(), &trace); err != nil {
		t.Fatal(err)
	}
	for _, ev := range trace.TraceEvents {
		if ev.Ph == "E" && ev.TS != 10_000 {
			t.Errorf("after Reset, closing timestamp = %v, want 10000", ev.TS)
		}
	}
}
