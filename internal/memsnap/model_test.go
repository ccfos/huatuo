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

package memsnap

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLimitOutputBoundsStringsAndEncodedBytes(t *testing.T) {
	large := strings.Repeat("x", MaxSnapshotBytes)
	snapshot := &Snapshot{
		RuntimeVersion: large,
		Status:         StatusPartial,
		Reason:         large,
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
		len(snapshot.Reason) > maxReasonBytes || len(snapshot.Entries[0].Name) > maxEntryNameBytes ||
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
	entries := make([]Entry, MaxTopK)
	for index := range entries {
		entries[index] = Entry{Name: "entry", Stack: append([]string(nil), stack...)}
	}
	snapshot := &Snapshot{Status: StatusComplete, Entries: entries}
	if err := LimitOutput(snapshot, MaxTopK); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > MaxSnapshotBytes || len(snapshot.Entries) >= MaxTopK ||
		!snapshot.OutputTruncated {
		t.Fatalf("bounded snapshot bytes=%d entries=%d truncated=%v",
			len(raw), len(snapshot.Entries), snapshot.OutputTruncated)
	}
}
