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

func TestCounterValuesRemainJSONEncodable(t *testing.T) {
	tests := []struct {
		value string
		want  any
	}{
		{"NaN", "NaN"},
		{"nan", "nan"},
		{"Inf", "Inf"},
		{"+Inf", "+Inf"},
		{"-Inf", "-Inf"},
		{"Infinity", "Infinity"},
		{"-Infinity", "-Infinity"},
		{"1e999", "1e999"},
		{"12.5", float64(12.5)},
		{"-2e3", float64(-2000)},
		{"0", float64(0)},
		{"unknown", "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			f := New(100)
			tags := map[string]string{"value": tt.value, "count": "3"}
			if err := f.Add(&output.Sample{Tags: tags}); err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			if err := f.Write(&buf); err != nil {
				t.Fatalf("Write with counter %q: %v", tt.value, err)
			}
			var trace traceOutput
			if err := json.Unmarshal(buf.Bytes(), &trace); err != nil {
				t.Fatal(err)
			}
			if len(trace.TraceEvents) != 1 || trace.TraceEvents[0].Ph != "C" {
				t.Fatalf("expected one counter event, got %+v", trace.TraceEvents)
			}
			args := trace.TraceEvents[0].Args
			if args["value"] != tt.want || args["count"] != float64(3) {
				t.Fatalf("counter args = %#v, want value=%#v and count=3", args, tt.want)
			}
			if tags["value"] != tt.value || tags["count"] != "3" {
				t.Fatalf("input tags changed: %#v", tags)
			}
		})
	}
}

func BenchmarkCounterEvent(b *testing.B) {
	sample := &output.Sample{Tags: map[string]string{"cpu": "12.5", "count": "3"}}
	b.ReportAllocs()
	for b.Loop() {
		counterEvent(sample, 0)
	}
}
