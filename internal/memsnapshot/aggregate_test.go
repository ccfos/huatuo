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
	"math"
	"testing"
)

// TestSaturatingAdd pins the overflow contract shared by the java provider
// aggregation paths (sampling, class aggregation, and heap totals): counters
// saturate at the maximum value instead of wrapping around.
func TestSaturatingAdd(t *testing.T) {
	tests := []struct {
		name        string
		left, right uint64
		want        uint64
	}{
		{name: "zero", left: 0, right: 0, want: 0},
		{name: "plain sum", left: 1, right: 2, want: 3},
		{name: "exact maximum", left: math.MaxUint64 - 1, right: 1, want: math.MaxUint64},
		{name: "single overflow", left: math.MaxUint64, right: 1, want: math.MaxUint64},
		{name: "large overflow", left: math.MaxUint64 - 1, right: 2, want: math.MaxUint64},
		{name: "both maximum", left: math.MaxUint64, right: math.MaxUint64, want: math.MaxUint64},
	}

	for _, tt := range tests {
		if got := SaturatingAdd(tt.left, tt.right); got != tt.want {
			t.Fatalf("%s: SaturatingAdd(%d, %d) = %d, want %d", tt.name, tt.left, tt.right, got, tt.want)
		}
	}
}

func TestEntriesFromObjects(t *testing.T) {
	objects := []ObjectAggregate{
		{TypeName: "java.lang.String", Count: 4, ShallowBytes: 1024, AverageBytes: 256},
		{TypeName: "byte[]", Count: 2, ShallowBytes: 4096, AverageBytes: 2048},
	}

	entries := EntriesFromObjects(objects)
	if len(entries) != len(objects) {
		t.Fatalf("len(EntriesFromObjects) = %d, want %d", len(entries), len(objects))
	}

	for i, object := range objects {
		entry := entries[i]
		if entry.Kind != "object_class" {
			t.Fatalf("entries[%d].Kind = %q, want %q", i, entry.Kind, "object_class")
		}

		if entry.Name != object.TypeName || entry.Bytes != object.ShallowBytes ||
			entry.Objects != object.Count || entry.AverageBytes != object.AverageBytes {
			t.Fatalf("entries[%d] = %+v, want fields from %+v", i, entry, object)
		}

		if entry.Stack != nil {
			t.Fatalf("entries[%d].Stack = %v, want nil for object aggregates", i, entry.Stack)
		}
	}
}

func TestEntriesFromObjectsEmpty(t *testing.T) {
	entries := EntriesFromObjects(nil)
	if entries == nil {
		t.Fatal("EntriesFromObjects(nil) = nil, want an empty slice")
	}

	if len(entries) != 0 {
		t.Fatalf("len(EntriesFromObjects(nil)) = %d, want 0", len(entries))
	}
}
