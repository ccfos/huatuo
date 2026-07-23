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

package types

import (
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestWatchEventJSONContract(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  map[string]any
	}{
		{
			name: "cloud events envelope uses canonical field names",
			value: WatchEvent{
				SpecVersion:     "1.0",
				ID:              "event-1",
				Source:          "huatuo-bamai",
				Type:            "io.tracing.complete",
				DataContentType: "application/json",
				Time:            "2026-07-22T00:00:00Z",
				Data: WatchEventData{
					Hostname:          "node-1",
					Region:            "test",
					ObservedTimestamp: "2026-07-22T00:00:00Z",
					TracerName:        "iotracing",
				},
			},
			want: map[string]any{
				"specversion":     "1.0",
				"id":              "event-1",
				"source":          "huatuo-bamai",
				"type":            "io.tracing.complete",
				"datacontenttype": "application/json",
				"time":            "2026-07-22T00:00:00Z",
				"data": map[string]any{
					"hostname":           "node-1",
					"region":             "test",
					"observed_timestamp": "2026-07-22T00:00:00Z",
					"tracer_name":        "iotracing",
				},
			},
		},
		{
			name:  "optional container fields are omitted",
			value: WatchEventData{Hostname: "node-1", Region: "test", ObservedTimestamp: "2026-07-22T00:00:00Z"},
			want: map[string]any{
				"hostname":           "node-1",
				"region":             "test",
				"observed_timestamp": "2026-07-22T00:00:00Z",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := json.Marshal(tt.value)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}

			var got map[string]any
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("JSON mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
