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

package memsnapshot

import (
	"encoding/json"
	"testing"
)

func TestRequestJSON(t *testing.T) {
	const input = `{"identity":{"tgid":42,"start_time_ticks":100},"sampling_seed":11,"max_memory_object_entries":7}`
	var request Request
	if err := json.Unmarshal([]byte(input), &request); err != nil {
		t.Fatal(err)
	}
	want := Request{
		Process:                ProcessInstanceID{TGID: 42, StartTimeTicks: 100},
		SamplingSeed:           11,
		MaxMemoryObjectEntries: 7,
	}
	if request != want {
		t.Fatalf("decoded request = %+v, want %+v", request, want)
	}

	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["max_memory_object_entries"]) != "7" {
		t.Fatalf("max_memory_object_entries = %s, want 7", fields["max_memory_object_entries"])
	}
	if _, present := fields["top_k"]; present {
		t.Fatalf("legacy top_k field emitted: %s", raw)
	}
}

func TestStatusReasonJSON(t *testing.T) {
	for _, test := range []struct {
		name   string
		status Status
		reason string
	}{
		{name: "complete", status: SnapshotStatusComplete},
		{name: "partial", status: SnapshotStatusPartial, reason: "scan budget reached"},
		{name: "unavailable", status: SnapshotStatusUnavailable, reason: "runtime is not supported"},
		{name: "failed", status: SnapshotStatusFailed, reason: "read memory: permission denied"},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, record := range []struct {
				name  string
				value any
			}{
				{name: "snapshot", value: Snapshot{Status: test.status, StatusReason: test.reason}},
				{name: "process memory", value: ProcessMemory{Status: test.status, StatusReason: test.reason}},
			} {
				t.Run(record.name, func(t *testing.T) {
					raw, err := json.Marshal(record.value)
					if err != nil {
						t.Fatal(err)
					}
					var fields map[string]any
					if err := json.Unmarshal(raw, &fields); err != nil {
						t.Fatal(err)
					}
					if fields["status"] != string(test.status) {
						t.Fatalf("status = %v, want %q", fields["status"], test.status)
					}
					reason, present := fields["status_reason"]
					if present != (test.reason != "") || (present && reason != test.reason) {
						t.Fatalf("status_reason = %v, present = %t; want %q, present = %t",
							reason, present, test.reason, test.reason != "")
					}
					if _, present := fields["reason"]; present {
						t.Fatalf("legacy reason field emitted: %s", raw)
					}
				})
			}
		})
	}
}
