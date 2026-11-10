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

// Package watch provides a generic fan-out pub/sub hub.
package watch

import "sync"

const defaultBufSize = 256

type subscriber[T any] struct {
	ch chan T
}

// Hub is a thread-safe fan-out pub/sub hub for values of type T.
// Each call to Subscribe returns a buffered channel; Notify fans out to all
// active subscribers. The zero value is ready for use and lazily adopts the
// default per-subscriber buffer size. Full subscriber channels are silently dropped.
type Hub[T any] struct {
	mu      sync.RWMutex
	subs    map[uint64]*subscriber[T]
	nextID  uint64
	bufSize int
}

// NewHub creates a Hub with the default per-subscriber buffer size.
func NewHub[T any]() *Hub[T] {
	return &Hub[T]{
		subs:    make(map[uint64]*subscriber[T]),
		bufSize: defaultBufSize,
	}
}

// Subscribe registers a new subscriber and returns a read-only channel and a
// cancel function. Calling cancel unregisters the subscriber.
func (h *Hub[T]) Subscribe() (<-chan T, func()) {
	h.mu.Lock()
	if h.subs == nil {
		h.subs = make(map[uint64]*subscriber[T])
	}
	if h.bufSize <= 0 {
		h.bufSize = defaultBufSize
	}

	id := h.nextID
	h.nextID++
	sub := &subscriber[T]{ch: make(chan T, h.bufSize)}
	h.subs[id] = sub

	h.mu.Unlock()

	return sub.ch, func() {
		h.mu.Lock()
		delete(h.subs, id)
		h.mu.Unlock()
	}
}

// Notify fans out v to every registered subscriber. Subscribers whose buffer
// is full are skipped without blocking.
func (h *Hub[T]) Notify(v T) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, sub := range h.subs {
		select {
		case sub.ch <- v:
		default:
		}
	}
}
