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

package golang

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"testing"
	"time"
)

func TestStackAggregatesOwnKeysAndRankTotals(t *testing.T) {
	groups := newStackAggr()
	stack := []byte("alpha")
	groups.addSample(stack, 2, 20, 1)
	copy(stack, "bravo")
	groups.addSample(stack, 1, 25, 1)
	groups.addSample([]byte("alpha"), 3, 30, 1)
	groups.addSample([]byte("delta"), 2, 25, 1)
	groups.addSample([]byte("charlie"), 2, 25, 1)
	got, err := groups.sortedAllocations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[0].key != "alpha" || got[0].inuseObjects != 5 ||
		got[0].inuseBytes != 50 || got[1].inuseBytes != 25 {
		t.Fatalf("ranked aggregates = %+v", got)
	}
	wantObjects, exists := map[string]int64{"bravo": 1, "charlie": 2, "delta": 2}[got[1].key]
	if !exists || got[1].inuseObjects != wantObjects {
		t.Fatalf("stack and counters do not match: %+v", got[1])
	}
	if len(groups.indices) != 4 || groups.keyBytes != 22 {
		t.Fatalf("aggregate keys were duplicated or lost: %+v", groups)
	}
}

func TestStackAggregatesBudget(t *testing.T) {
	groups := newStackAggr()
	groups.addSample([]byte("a"), 2, 20, 1)
	groups.keyBytes = maxAggregateKeyBytes
	if groups.addSample([]byte("b"), 1, 1, 1) {
		t.Fatal("retained a new key beyond the budget")
	}
	if !groups.addSample([]byte("a"), 1, 10, 1) {
		t.Fatal("rejected an existing key at the budget")
	}
	got, err := groups.sortedAllocations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].inuseObjects != 3 || got[0].inuseBytes != 30 {
		t.Fatalf("totals at the key budget = %+v", got)
	}
}

func TestStackAggregatesCounterOverflow(t *testing.T) {
	for _, test := range []struct {
		name    string
		samples []uint64
		want    int64
	}{
		{name: "conversion", samples: []uint64{math.MaxUint64}, want: -1},
		{name: "accumulation", samples: []uint64{math.MaxInt64, 1}, want: math.MinInt64},
	} {
		t.Run(test.name, func(t *testing.T) {
			groups := newStackAggr()
			for _, count := range test.samples {
				groups.addSample([]byte("stack"), count, count, 1)
			}
			got, err := groups.sortedAllocations(t.Context())
			if err != nil || len(got) != 1 || got[0].inuseObjects != test.want || got[0].inuseBytes != test.want {
				t.Fatalf("counter overflow = %+v, %v; want %d", got, err, test.want)
			}
		})
	}
}

func TestStackAggregatesCorrectSamplesBeforeMerging(t *testing.T) {
	groups := newStackAggr()
	stack := []byte("same stack")
	if !groups.addSample(stack, 1, 1, 100) || !groups.addSample(stack, 1, 100, 100) {
		t.Fatal("samples unexpectedly exceeded the key budget")
	}
	copy(stack, "overwritten")
	got, err := groups.sortedAllocations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// Independent per-sample corrections truncate to (100,100) and (1,158).
	// Correcting the combined (2,101) sample would produce different totals.
	if len(got) != 1 || got[0].key != "same stack" || got[0].inuseObjects != 101 || got[0].inuseBytes != 258 {
		t.Fatalf("corrected aggregate = %+v", got)
	}
}

func TestStackAggregatesSortedAllocations(t *testing.T) {
	for _, count := range []int{0, 1, 32, 257} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			groups := newStackAggr()
			samples := make(map[string]allocation)
			var want []int64
			for i := range count {
				entry := allocation{
					key: fmt.Sprintf("stack-%04d", i), inuseBytes: int64(i%7 + 1), inuseObjects: int64(i + 1),
				}
				groups.addSample([]byte(entry.key), uint64(entry.inuseObjects), uint64(entry.inuseBytes), 1)
				samples[entry.key] = entry
				want = append(want, entry.inuseBytes)
			}
			// Only weights define ordering; tied stacks may appear in any order.
			sort.Slice(want, func(i, j int) bool { return want[i] > want[j] })
			got, err := groups.sortedAllocations(t.Context())
			if err != nil || len(got) != count {
				t.Fatalf("sorted allocations = %+v, %v; want %d entries", got, err, count)
			}
			seen := make(map[string]bool)
			for i, entry := range got {
				if entry.inuseBytes != want[i] || entry != samples[entry.key] || seen[entry.key] {
					t.Fatalf("invalid entry at %d: %+v; want weight %d and unique matching sample", i, entry, want[i])
				}
				seen[entry.key] = true
			}
		})
	}
}

func TestStackAggregatesSortedWeights(t *testing.T) {
	for _, test := range []struct {
		name   string
		values []int64
		want   []int64
	}{
		{name: "mixed", values: []int64{90, 100, 90}, want: []int64{100, 90, 90}},
		{name: "all tied", values: []int64{90, 90, 90}, want: []int64{90, 90, 90}},
		{name: "zero", values: []int64{0, 100, 0}, want: []int64{100, 0, 0}},
		{name: "negative", values: []int64{-2, -1, -2}, want: []int64{-1, -2, -2}},
		{name: "minimum", values: []int64{math.MinInt64, -1, math.MinInt64}, want: []int64{-1, math.MinInt64, math.MinInt64}},
	} {
		t.Run(test.name, func(t *testing.T) {
			groups := newStackAggr()
			for i, value := range test.values {
				groups.addSample([]byte(fmt.Sprint(i)), 1, uint64(value), 1)
			}
			got, err := groups.sortedAllocations(t.Context())
			if err != nil || len(got) != len(test.want) {
				t.Fatalf("sorted allocations = %+v, %v", got, err)
			}
			for i, entry := range got {
				if entry.inuseBytes != test.want[i] {
					t.Fatalf("sorted allocations = %+v; want weights %v", got, test.want)
				}
			}
		})
	}
}

func TestStackAggregatesSortedAllocationsPreserveState(t *testing.T) {
	groups := newStackAggr()
	groups.addSample([]byte("a"), 1, 1, 1)
	groups.addSample([]byte("b"), 1, 2, 1)
	groups.addSample([]byte("c"), 1, 3, 1)
	want := []allocation{
		{key: "c", inuseBytes: 3, inuseObjects: 1},
		{key: "b", inuseBytes: 2, inuseObjects: 1},
		{key: "a", inuseBytes: 1, inuseObjects: 1},
	}
	for range 2 {
		got, err := groups.sortedAllocations(t.Context())
		if err != nil || !slices.Equal(got, want) {
			t.Fatalf("sorted allocations = %+v, %v", got, err)
		}
		// Callers may mutate results without corrupting later queries or updates.
		got[0] = allocation{key: "overwritten"}
	}
	groups.addSample([]byte("a"), 1, 5, 1)
	got, err := groups.sortedAllocations(t.Context())
	if err != nil || len(got) != 3 || got[0] != (allocation{key: "a", inuseBytes: 6, inuseObjects: 2}) {
		t.Fatalf("query changed aggregate state: %+v, %v", got, err)
	}
}

func TestScaleHeapSample(t *testing.T) {
	for _, test := range []struct {
		name                   string
		count, size, rate      int64
		wantObjects, wantBytes int64
	}{
		{name: "zero objects", size: 128, rate: 100},
		{name: "zero bytes", count: 1, rate: 100},
		{
			name: "no scaling", count: math.MaxInt64, size: math.MaxInt64, rate: 1,
			wantObjects: math.MaxInt64, wantBytes: math.MaxInt64,
		},
		{name: "ordinary", count: 1, size: 1, rate: 100, wantObjects: 100, wantBytes: 100},
		{
			name: "rounded integer limit", count: 1, size: math.MaxInt64, rate: 2,
			wantObjects: 1, wantBytes: math.MaxInt64,
		},
		{
			name: "finite overflow", count: 1, size: math.MaxInt64, rate: math.MaxInt64,
			wantObjects: 1, wantBytes: math.MaxInt64,
		},
		{
			name: "infinite estimate", count: 1, size: 1, rate: math.MaxInt64,
			wantObjects: math.MaxInt64, wantBytes: math.MaxInt64,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			objects, bytes := scaleHeapSample(test.count, test.size, test.rate)
			if objects != test.wantObjects || bytes != test.wantBytes {
				t.Fatalf("scaled sample = (%d, %d), want (%d, %d)", objects, bytes, test.wantObjects, test.wantBytes)
			}
		})
	}
}

func TestStackAggregatesCancellation(t *testing.T) {
	for _, populated := range []bool{false, true} {
		for _, expired := range []bool{false, true} {
			t.Run(fmt.Sprintf("populated=%t/expired=%t", populated, expired), func(t *testing.T) {
				groups := newStackAggr()
				if populated {
					groups.addSample([]byte("stack"), 1, 128, 1)
				}
				ctx, cancel := context.WithCancel(t.Context())
				want := context.Canceled
				if expired {
					cancel()
					ctx, cancel = context.WithDeadline(t.Context(), time.Unix(1, 0))
					want = context.DeadlineExceeded
				}
				cancel()
				if got, err := groups.sortedAllocations(ctx); got != nil || !errors.Is(err, want) {
					t.Fatalf("canceled allocations = %+v, %v; want no allocations and %v", got, err, want)
				}
			})
		}
	}
}

func TestStackAggregatesCancellationDuringResults(t *testing.T) {
	groups := newStackAggr()
	for i := range 64 {
		groups.addSample([]byte(fmt.Sprint(i)), 1, uint64(i+1), 1)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	queryCtx := &cancelingAggregateContext{Context: ctx, cancel: cancel, remaining: 6}
	got, err := groups.sortedAllocations(queryCtx)
	if got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled allocations = %+v, %v", got, err)
	}
}

// Cancel while collecting results without depending on scheduler timing.
type cancelingAggregateContext struct {
	context.Context
	cancel    context.CancelFunc
	remaining int
}

func (c *cancelingAggregateContext) Err() error {
	c.remaining--
	if c.remaining == 0 {
		c.cancel()
	}
	return c.Context.Err()
}
