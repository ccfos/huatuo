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
	"strings"
	"testing"
)

func TestLimitOutputBoundsStringsAndEncodedBytes(t *testing.T) {
	large := strings.Repeat("x", MaxSnapshotBytes)
	snapshot := &Snapshot{
		RuntimeVersion: large,
		Status:         SnapshotStatusPartial,
		StatusReason:   large,
		Entries: []Entry{
			{Kind: large, Name: large, Stack: []string{large}},
			{Kind: large, Name: large, Stack: []string{large}},
		},
	}
	if err := LimitOutput(snapshot, 2); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > MaxSnapshotBytes {
		t.Fatalf("encoded snapshot bytes = %d, want <= %d", len(raw), MaxSnapshotBytes)
	}
	if !snapshot.OutputTruncated || len(snapshot.RuntimeVersion) > maxRuntimeVersionBytes ||
		len(snapshot.StatusReason) > maxStatusReasonBytes || len(snapshot.Entries[0].Name) > maxEntryNameBytes ||
		len(snapshot.Entries[0].Stack[0]) > maxStackFrameBytes {
		t.Fatalf("snapshot was not bounded: %+v", snapshot)
	}
}

func TestLimitOutputDropsEntriesToEncodedLimit(t *testing.T) {
	frame := strings.Repeat("x", maxStackFrameBytes)
	stack := make([]string, maxStackFrames)
	for index := range stack {
		stack[index] = frame
	}
	entries := make([]Entry, MaxMemoryObjectEntries)
	for index := range entries {
		entries[index] = Entry{Name: "entry", Stack: append([]string(nil), stack...)}
	}
	snapshot := &Snapshot{Status: SnapshotStatusComplete, Entries: entries}
	if err := LimitOutput(snapshot, MaxMemoryObjectEntries); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > MaxSnapshotBytes || len(snapshot.Entries) >= MaxMemoryObjectEntries ||
		!snapshot.OutputTruncated {
		t.Fatalf("bounded snapshot bytes=%d entries=%d truncated=%v",
			len(raw), len(snapshot.Entries), snapshot.OutputTruncated)
	}
}

func TestOutputTruncatedJSONCompatibility(t *testing.T) {
	for _, omitted := range []bool{false, true} {
		snapshot := Snapshot{Status: SnapshotStatusComplete, OutputTruncated: omitted}
		raw, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		value, present := fields["output_truncated"]
		if present != omitted || (present && string(value) != "true") {
			t.Fatalf("legacy JSON key = %s", raw)
		}
		if _, present := fields["has_omitted_data"]; present {
			t.Fatalf("unexpected JSON key migration: %s", raw)
		}
		var decoded Snapshot
		if err := json.Unmarshal(raw, &decoded); err != nil || decoded.OutputTruncated != omitted {
			t.Fatalf("round trip = %+v, %v", decoded, err)
		}
	}
}
