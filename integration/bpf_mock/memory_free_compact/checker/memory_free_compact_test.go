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

//go:build integration

package main

import "testing"

func TestCompactionLatencyBounds(t *testing.T) {
	metrics := func(reclaim, compact string) string {
		return "huatuo_bamai_memory_free_allocpages_stall{host=\"test\"} " + reclaim + "\nhuatuo_bamai_memory_free_compaction_stall{host=\"test\"} " + compact + "\n"
	}
	for _, tc := range []struct {
		name, before, after string
		wantError           bool
	}{
		{"bounds", metrics("0", "0"), metrics("40", "45"), false},
		{"too short", metrics("0", "0"), metrics("39", "45"), true},
		{"too long", metrics("0", "0"), metrics("40", "51"), true},
		{"nonzero baseline", metrics("1", "0"), metrics("40", "45"), true},
		{"missing", metrics("0", "0"), "", true},
		{"NaN", metrics("0", "0"), metrics("NaN", "45"), true},
		{"infinite", metrics("0", "0"), metrics("Inf", "45"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := verifyLatencyMetrics(tc.before, tc.after, 50000000, 50000000); (err != nil) != tc.wantError {
				t.Fatalf("error: %v", err)
			}
		})
	}
}

func TestUnmatchedReclaimEnd(t *testing.T) {
	before := "huatuo_bamai_memory_free_allocpages_stall{} 40\nhuatuo_bamai_memory_free_compaction_stall{} 45\n"
	if err := verifyUnmatchedReclaimEnd(before, before); err != nil {
		t.Fatal(err)
	}
	after := "huatuo_bamai_memory_free_allocpages_stall{} 41\nhuatuo_bamai_memory_free_compaction_stall{} 45\n"
	if err := verifyUnmatchedReclaimEnd(before, after); err == nil {
		t.Fatal("unmatched end changed latency")
	}
}
