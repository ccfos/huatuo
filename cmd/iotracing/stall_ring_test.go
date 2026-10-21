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

package main

import (
	"slices"
	"testing"

	"github.com/ccfos/huatuo/pkg/types"
)

func TestStallRingNoEventsLargeLimit(t *testing.T) {
	ring := newStallRing(1 << 62)
	if got := len(ring.ordered()); got != 0 {
		t.Fatalf("ordered events = %d, want 0", got)
	}
	if got := cap(ring.samples); got > int(initialStallRingCapacity) {
		t.Fatalf("initial capacity = %d, want at most %d", got, initialStallRingCapacity)
	}
}

func TestStallRingGrowsOnlyForObservedEvents(t *testing.T) {
	ring := newStallRing(1 << 62)
	for id := uint32(1); id <= 100; id++ {
		ring.add(&types.IOScheduleEvent{PID: id})
	}
	got := ring.ordered()
	if len(got) != 100 {
		t.Fatalf("ordered events = %d, want 100", len(got))
	}
	for i, event := range got {
		if event.PID != uint32(i+1) {
			t.Fatalf("event %d PID = %d, want %d", i, event.PID, i+1)
		}
	}
}

func TestStallRingPreservesRecentEventOrder(t *testing.T) {
	tests := []struct {
		name  string
		limit uint64
		count uint32
		want  []uint32
	}{
		{name: "zero limit", limit: 0, count: 3},
		{name: "partial", limit: 3, count: 2, want: []uint32{1, 2}},
		{name: "exactly full", limit: 3, count: 3, want: []uint32{1, 2, 3}},
		{name: "wrapped", limit: 3, count: 5, want: []uint32{3, 4, 5}},
		{name: "wrapped to start", limit: 3, count: 6, want: []uint32{4, 5, 6}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ring := newStallRing(tt.limit)
			for id := uint32(1); id <= tt.count; id++ {
				ring.add(&types.IOScheduleEvent{PID: id})
			}
			got := make([]uint32, 0, len(ring.ordered()))
			for _, event := range ring.ordered() {
				got = append(got, event.PID)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("event order = %v, want %v", got, tt.want)
			}
		})
	}
}

var benchmarkStallRingResult []types.IOScheduleEvent

func BenchmarkStallRing(b *testing.B) {
	for _, tc := range []struct {
		name  string
		limit uint64
		count int
	}{
		{name: "large_limit_no_events", limit: 1 << 40},
		{name: "default_limit_wrapped", limit: 10, count: 20},
		{name: "large_limit_32_events", limit: 1 << 40, count: 32},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				ring := newStallRing(tc.limit)
				for id := 0; id < tc.count; id++ {
					ring.add(&types.IOScheduleEvent{PID: uint32(id)})
				}
				benchmarkStallRingResult = ring.ordered()
			}
		})
	}
}
