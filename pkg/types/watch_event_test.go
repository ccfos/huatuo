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
)

func TestWatchEventUsesCloudEventsJSONFieldNames(t *testing.T) {
	event := WatchEvent{
		SpecVersion:     "1.0",
		ID:              "event-1",
		Source:          "huatuo-bamai",
		Type:            "io.tracing.complete",
		DataContentType: "application/json",
		Time:            "2026-07-22T00:00:00Z",
		Data: WatchEventData{
			Hostname:  "node-1",
			Region:    "test",
			TracerName: "iotracing",
		},
	}

	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	for _, field := range []string{"specversion", "id", "source", "type", "datacontenttype", "time", "data"} {
		if _, ok := fields[field]; !ok {
			t.Errorf("event JSON is missing %q: %s", field, encoded)
		}
	}
	if _, ok := fields["SpecVersion"]; ok {
		t.Errorf("event JSON unexpectedly exposes Go field name: %s", encoded)
	}
}

func TestWatchEventDataOmitsOptionalContainerFields(t *testing.T) {
	encoded, err := json.Marshal(WatchEventData{Hostname: "node-1", Region: "test"})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if _, ok := fields["container_id"]; ok {
		t.Errorf("empty optional container_id was serialized: %s", encoded)
	}
}
