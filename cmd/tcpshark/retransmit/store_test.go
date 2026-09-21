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

package retransmit

import (
	"fmt"
	"net/netip"
	"slices"
	"testing"
	"time"
)

func TestStoreCapacity(t *testing.T) {
	for _, capacity := range []int{1, retransmitQueueCapacity, dropwatchQueueCapacity} {
		t.Run(fmt.Sprint(capacity), func(t *testing.T) {
			store := newStore[int](capacity, time.Second)
			flow := testFlowKey(1000, 80)
			now := time.Unix(1, 0)
			first := &storeEntry[int]{}
			store.add(first, &flow, now)
			for index := 1; index <= capacity; index++ {
				entry := &storeEntry[int]{value: index}
				evicted := store.add(entry, &flow, now)
				if index < capacity && evicted != nil {
					t.Fatalf("add(%d) evicted before capacity", index)
				}
				if index == capacity && evicted != first {
					t.Fatalf("capacity eviction = %p, want first %p", evicted, first)
				}
				if entry.sequence != uint64(index+1) {
					t.Fatalf("sequence = %d, want %d", entry.sequence, index+1)
				}
			}
			if store.byDeadline.Len() != capacity || len(store.entriesForFlow(flow)) != capacity {
				t.Fatalf("capacity state = deadlines %d flow %d, want %d",
					store.byDeadline.Len(), len(store.entriesForFlow(flow)), capacity)
			}
			if first.node != nil || slices.Contains(store.entriesForFlow(flow), first) {
				t.Fatal("evicted entry remains indexed")
			}
		})
	}
}

func TestStoreCanonicalFlowIndex(t *testing.T) {
	for _, endpoints := range [][2]string{
		{"10.0.0.1:1000", "10.0.0.2:80"},
		{"[2001:db8::1]:1000", "[2001:db8::2]:80"},
		{"127.0.0.1:1000", "127.0.0.1:80"},
		{"127.0.0.1:1000", "127.0.0.1:1000"},
	} {
		t.Run(endpoints[0]+"/"+endpoints[1], func(t *testing.T) {
			store := newStore[flowKey](2, time.Second)
			flow := flowKey{
				source: netip.MustParseAddrPort(endpoints[0]), destination: netip.MustParseAddrPort(endpoints[1]),
			}
			reverse := reverseFlow(flow)
			first := &storeEntry[flowKey]{value: flow}
			second := &storeEntry[flowKey]{value: reverse}
			now := time.Unix(1, 0)
			store.add(first, &flow, now)
			store.add(second, &reverse, now)
			want := []*storeEntry[flowKey]{first, second}
			if len(store.byFlow) != 1 || !slices.Equal(store.entriesForFlow(flow), want) ||
				!slices.Equal(store.entriesForFlow(reverse), want) {
				t.Fatal("opposite directions do not share exactly one flow bucket")
			}
			if first.value != flow || second.value != reverse {
				t.Fatal("indexing changed payload direction")
			}
		})
	}
}

func TestStoreRemoveClearsBothIndexes(t *testing.T) {
	store := newStore[int](4, time.Second)
	flow := testFlowKey(1000, 80)
	reverse := reverseFlow(flow)
	now := time.Unix(1, 0)
	first, middle, last := &storeEntry[int]{}, &storeEntry[int]{}, &storeEntry[int]{}
	store.add(first, &flow, now)
	store.add(middle, &reverse, now)
	store.add(last, &flow, now)
	otherFlow := testFlowKey(2000, 80)
	other := &storeEntry[int]{}
	store.add(other, &otherFlow, now)

	bucket := store.entriesForFlow(flow)
	if store.remove(middle) != middle || middle.node != nil {
		t.Fatal("remove did not detach the selected entry")
	}
	if bucket[len(bucket)-1] != nil {
		t.Fatal("removed entry retained in flow slice backing array")
	}
	if !slices.Equal(store.entriesForFlow(flow), []*storeEntry[int]{first, last}) || store.byDeadline.Len() != 3 {
		t.Fatal("middle removal changed other entries")
	}
	store.remove(first)
	store.remove(last)
	if len(store.entriesForFlow(flow)) != 0 || len(store.byFlow) != 1 || store.byDeadline.Len() != 1 {
		t.Fatal("empty flow bucket remained after its final removal")
	}
	if store.byDeadline.Front().Value != other || store.entriesForFlow(otherFlow)[0] != other {
		t.Fatal("removal affected an unrelated flow")
	}
}

func TestStoreExpirationAndDrain(t *testing.T) {
	store := newStore[int](3, time.Second)
	now := time.Unix(1, 0)
	if _, ok := store.nextDeadline(); ok || store.takeExpired(now) != nil || store.drain() != nil {
		t.Fatal("empty store returned an entry or deadline")
	}
	flow := testFlowKey(1000, 80)
	reverse := reverseFlow(flow)
	otherFlow := testFlowKey(2000, 80)
	first, second, third := &storeEntry[int]{}, &storeEntry[int]{}, &storeEntry[int]{}
	store.add(first, &flow, now)
	store.add(second, &reverse, now.Add(time.Millisecond))
	store.add(third, &otherFlow, now.Add(time.Millisecond))
	deadline := now.Add(time.Second)
	if got, ok := store.nextDeadline(); !ok || got != deadline {
		t.Fatalf("nextDeadline() = (%s, %t), want (%s, true)", got, ok, deadline)
	}
	if store.takeExpired(deadline.Add(-time.Nanosecond)) != nil {
		t.Fatal("entry expired before its deadline")
	}
	if store.takeExpired(deadline) != first || store.takeExpired(deadline) != nil {
		t.Fatal("expiration did not respect the exact deadline")
	}
	if got, ok := store.nextDeadline(); !ok || got != second.deadline {
		t.Fatalf("nextDeadline() = (%s, %t), want second deadline", got, ok)
	}
	if got := store.drain(); !slices.Equal(got, []*storeEntry[int]{second, third}) {
		t.Fatalf("drain() = %v, want remaining entries in arrival order", got)
	}
	if store.byDeadline.Len() != 0 || len(store.byFlow) != 0 || store.drain() != nil {
		t.Fatal("drained store retained entries")
	}
	if first.node != nil || second.node != nil || third.node != nil {
		t.Fatal("expiration or drain retained list membership")
	}
}
