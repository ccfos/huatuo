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
	"os"
	"path/filepath"
	"testing"

	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/pkg/types"
)

func fractionalWindow(unit string, values ...int64) *types.AggregationWindow {
	w := &types.AggregationWindow{
		ProfileType: "process_cpu:cpu:" + unit + ":cpu:" + unit,
		Aggregated: types.AggregatedProfile{
			TimeNanos:     1,
			DurationNanos: 1,
			Frames:        []types.ProfileFrame{{Name: "root"}, {Name: "leaf"}},
			Stacks:        [][]uint32{{0, 1}},
		},
	}
	for _, value := range values {
		w.Aggregated.Samples = append(w.Aggregated.Samples, types.AggregatedSample{UserStack: 1, Value: value})
	}
	return w
}

func fractionalFormatter(t *testing.T) (output.Formatter, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trace.json")
	f, err := NewFormatter(output.Config{Destination: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	})
	return f, path
}

type decodedEvent struct {
	Name     string      `json:"name"`
	Phase    string      `json:"ph"`
	Time     json.Number `json:"ts"`
	Duration json.Number `json:"dur"`
}

func fractionalEvents(t *testing.T, f output.Formatter, path string) []decodedEvent {
	t.Helper()
	if err := f.Complete(t.Context()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var trace struct {
		Events []decodedEvent `json:"traceEvents"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&trace); err != nil {
		t.Fatal(err)
	}
	var events []decodedEvent
	for _, item := range trace.Events {
		if item.Phase == "X" {
			events = append(events, item)
		}
	}
	return events
}

func TestFormatterFractionalDuration(t *testing.T) {
	for _, test := range []struct {
		unit  string
		value int64
		want  string
	}{
		{"nanoseconds", 1, "0.001"},
		{"nanoseconds", 500, "0.5"},
		{"nanoseconds", 999, "0.999"},
		{"nanoseconds", 1000, "1"},
		{"nanoseconds", 1001, "1.001"},
		{"nanoseconds", 1500, "1.5"},
		{"nanoseconds", 999999, "999.999"},
		{"microseconds", 1, "1"},
		{"milliseconds", 1, "1000"},
		{"seconds", 1, "1000000"},
	} {
		t.Run(test.unit+"/"+test.want, func(t *testing.T) {
			f, path := fractionalFormatter(t)
			if err := f.Add(t.Context(), fractionalWindow(test.unit, test.value)); err != nil {
				t.Fatal(err)
			}
			events := fractionalEvents(t, f, path)
			if len(events) != 2 {
				t.Fatalf("events = %v", events)
			}
			for i, item := range events {
				if item.Duration.String() != test.want || item.Time.String() != "0" {
					t.Fatalf("frame %d: timestamp=%s duration=%s; want 0/%s", i, item.Time, item.Duration, test.want)
				}
			}
		})
	}
}

func TestFormatterFractionalAcrossWindows(t *testing.T) {
	f, path := fractionalFormatter(t)
	for _, w := range []*types.AggregationWindow{
		fractionalWindow("nanoseconds", 1, 499),
		fractionalWindow("microseconds", 1),
		fractionalWindow("nanoseconds", 1),
		fractionalWindow("milliseconds", 2),
	} {
		if err := f.Add(t.Context(), w); err != nil {
			t.Fatal(err)
		}
	}
	events := fractionalEvents(t, f, path)
	wantTimes := []string{"0", "0.001", "0.5", "1.5", "1.501"}
	wantDurations := []string{"0.001", "0.499", "1", "0.001", "2000"}
	if len(events) != 2*len(wantTimes) {
		t.Fatalf("events = %v", events)
	}
	for i := range wantTimes {
		for _, item := range events[2*i : 2*i+2] {
			if item.Time.String() != wantTimes[i] || item.Duration.String() != wantDurations[i] {
				t.Fatalf("sample %d timestamp=%s duration=%s; want %s/%s", i, item.Time, item.Duration, wantTimes[i], wantDurations[i])
			}
		}
	}
}

func TestFormatterFractionalNoAccumulationLoss(t *testing.T) {
	f, path := fractionalFormatter(t)
	values := make([]int64, 1000)
	for i := range values {
		values[i] = 1
	}
	if err := f.Add(t.Context(), fractionalWindow("nanoseconds", values...)); err != nil {
		t.Fatal(err)
	}
	if err := f.Add(t.Context(), fractionalWindow("nanoseconds", 1)); err != nil {
		t.Fatal(err)
	}
	events := fractionalEvents(t, f, path)
	if len(events) != 2002 {
		t.Fatalf("event count=%d", len(events))
	}
	for _, item := range events {
		if item.Duration.String() != "0.001" {
			t.Fatalf("duration=%s; want 0.001", item.Duration)
		}
	}
	if events[1998].Time.String() != "0.999" || events[2000].Time.String() != "1" {
		t.Fatalf("last timestamp=%s next window=%s; want 0.999 and 1", events[1998].Time, events[2000].Time)
	}
}

func TestFormatterFractionalTimelineLimit(t *testing.T) {
	f, path := fractionalFormatter(t)
	if err := f.Add(t.Context(), fractionalWindow("microseconds", 1<<53-2)); err != nil {
		t.Fatal(err)
	}
	if err := f.Add(t.Context(), fractionalWindow("nanoseconds", 999, 1)); err != nil {
		t.Fatal(err)
	}
	if err := f.Add(t.Context(), fractionalWindow("nanoseconds", 1)); err == nil {
		t.Fatal("accepted time beyond the existing 2^53-1 microsecond limit")
	}
	events := fractionalEvents(t, f, path)
	if len(events) != 6 || events[4].Time.String() != "9007199254740990.999" || events[4].Duration.String() != "0.001" {
		t.Fatalf("boundary events=%v", events)
	}
}

func TestFormatterFractionalRejectsInvalidWeights(t *testing.T) {
	for _, test := range []struct {
		unit  string
		value int64
	}{
		{"bytes", 1}, {"count", 1}, {"nanoseconds", -1}, {"seconds", 1<<63 - 1},
	} {
		t.Run(test.unit, func(t *testing.T) {
			f, path := fractionalFormatter(t)
			if err := f.Add(t.Context(), fractionalWindow(test.unit, test.value)); err == nil {
				t.Fatal("accepted invalid duration")
			}
			if err := f.Complete(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("empty output stat=%v", err)
			}
		})
	}
}

func BenchmarkFormatterFractionalAdd(b *testing.B) {
	f := &formatter{}
	w := fractionalWindow("nanoseconds", 1500)
	b.ReportAllocs()
	for b.Loop() {
		events := f.events[:0]
		*f = formatter{events: events}
		if err := f.Add(b.Context(), w); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFormatterFractionalEncode(b *testing.B) {
	f := &formatter{}
	if err := f.Add(b.Context(), fractionalWindow("nanoseconds", 1500)); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := json.Marshal(f.events); err != nil {
			b.Fatal(err)
		}
	}
}
