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
	"container/list"
	"slices"
	"time"
)

type storeEntry[T any] struct {
	value    T
	flow     *flowKey
	deadline time.Time
	sequence uint64
	node     *list.Element
}

// A single correlator owns each store and expires entries before matching or
// adding. Fixed TTL and nondecreasing processing times keep deadlines ordered.
// The store and its entries must not be copied after insertion.
type store[T any] struct {
	byDeadline   list.List
	byFlow       map[flowKey][]*storeEntry[T]
	capacity     int
	ttl          time.Duration
	nextSequence uint64
}

func newStore[T any](capacity int, ttl time.Duration) store[T] {
	if capacity <= 0 {
		panic("store capacity must be positive")
	}
	if ttl <= 0 {
		panic("store TTL must be positive")
	}
	return store[T]{
		byFlow:   make(map[flowKey][]*storeEntry[T]),
		capacity: capacity,
		ttl:      ttl,
	}
}

// add takes ownership of entry and returns the earliest deadline evicted at
// capacity. The borrowed flow must remain unchanged until removal; pointing into
// the immutable event avoids copying its addresses into every store entry.
func (s *store[T]) add(entry *storeEntry[T], flow *flowKey, now time.Time) *storeEntry[T] {
	var evicted *storeEntry[T]
	if s.byDeadline.Len() == s.capacity {
		evicted = s.remove(s.byDeadline.Front().Value.(*storeEntry[T]))
	}
	s.nextSequence++
	entry.flow = flow
	entry.deadline = now.Add(s.ttl)
	entry.sequence = s.nextSequence
	entry.node = s.byDeadline.PushBack(entry)
	key := canonicalFlow(*flow)
	s.byFlow[key] = append(s.byFlow[key], entry)
	return evicted
}

// entriesForFlow borrows the flow bucket until the next store mutation.
func (s *store[T]) entriesForFlow(flow flowKey) []*storeEntry[T] {
	return s.byFlow[canonicalFlow(flow)]
}

// remove requires a live entry owned by this store.
func (s *store[T]) remove(entry *storeEntry[T]) *storeEntry[T] {
	key := canonicalFlow(*entry.flow)
	entries := s.byFlow[key]
	for index, stored := range entries {
		if stored != entry {
			continue
		}
		entries = slices.Delete(entries, index, index+1)
		if len(entries) == 0 {
			delete(s.byFlow, key)
		} else {
			s.byFlow[key] = entries
		}
		break
	}
	s.byDeadline.Remove(entry.node)
	entry.node = nil
	return entry
}

func (s *store[T]) takeExpired(now time.Time) *storeEntry[T] {
	front := s.byDeadline.Front()
	if front == nil {
		return nil
	}
	entry := front.Value.(*storeEntry[T])
	if now.Before(entry.deadline) {
		return nil
	}
	return s.remove(entry)
}

func (s *store[T]) nextDeadline() (time.Time, bool) {
	front := s.byDeadline.Front()
	if front == nil {
		return time.Time{}, false
	}
	return front.Value.(*storeEntry[T]).deadline, true
}

func (s *store[T]) drain() []*storeEntry[T] {
	if s.byDeadline.Len() == 0 {
		return nil
	}
	entries := make([]*storeEntry[T], 0, s.byDeadline.Len())
	for front := s.byDeadline.Front(); front != nil; front = s.byDeadline.Front() {
		entries = append(entries, s.remove(front.Value.(*storeEntry[T])))
	}
	return entries
}
