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

package pod

import "context"

// PodKey identifies one workload instance within this agent process.
// Generations increase across manager restarts but are not persisted.
type PodKey struct {
	ID         string
	Generation uint64
}

type PodEventKind uint8

const (
	PodEventUnknown PodEventKind = iota
	PodCreated
	PodDeleted
)

type PodEvent struct {
	Kind PodEventKind
	Key  PodKey
	Pod  *Pod
}

type PodUpdateMode uint8

const (
	PodUpdateUnknown PodUpdateMode = iota
	PodUpdateFull
	PodUpdateIncremental
)

// PodUpdate separates discovery health from cached workload availability.
// Events are read-only until the next DrainEvents call. Empty full updates
// confirm absence; empty incremental updates do not change membership.
type PodUpdate struct {
	Revision       uint64
	Mode           PodUpdateMode
	Events         []PodEvent
	DiscoveryError error
}

var (
	ErrPodsUnavailable       = ErrContainersUnavailable
	ErrPodSubscriptionClosed = ErrContainerSubscriptionClosed
	ErrPodManagerDisabled    = ErrContainerManagerDisabled
)

// PodSubscription shares the bounded lifecycle producer with legacy clients.
// DrainEvents has one consumer; Close and context cancellation may overlap it.
type PodSubscription struct {
	subscription
	batch []PodEvent
}

type subscription struct {
	store          *containerStore
	wake           chan struct{}
	stop           func() bool
	queue          [containerEventQueueSize]podEvent
	head           int
	count          int
	needsFull      bool
	isClosed       bool
	containersOnly bool
}

type podEvent struct {
	kind     PodEventKind
	ref      ContainerRef
	pod      *Pod
	revision uint64
}

func SubscribePods(ctx context.Context) (*PodSubscription, error) {
	return containerView.subscribePods(ctx)
}

func (v *containerStore) subscribePods(ctx context.Context) (*PodSubscription, error) {
	s := &PodSubscription{batch: make([]PodEvent, 0, containerEventBatchSize)}
	if err := v.addSubscription(ctx, &s.subscription, false); err != nil {
		return nil, err
	}
	return s, nil
}

func (v *containerStore) addSubscription(ctx context.Context, s *subscription, containersOnly bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.isActive {
		return ErrPodManagerDisabled
	}
	s.store, s.wake = v, make(chan struct{}, 1)
	s.needsFull, s.containersOnly = true, containersOnly
	v.subscribers[s] = struct{}{}
	s.stop = context.AfterFunc(ctx, s.Close)
	s.signal()
	return nil
}

// Notify coalesces readiness signals. Call DrainEvents after every wakeup;
// a closed channel means the subscription has terminated.
func (s *subscription) Notify() <-chan struct{} { return s.wake }

func (s *PodSubscription) DrainEvents() (PodUpdate, error) {
	clear(s.batch)
	s.batch = s.batch[:0]
	update, err := s.drain(func(kind PodEventKind, ref ContainerRef, workload *Pod) {
		s.batch = append(s.batch, PodEvent{Kind: kind, Key: ref.Key, Pod: workload})
	})
	if err != nil {
		return PodUpdate{}, err
	}
	update.Events = s.batch[:len(s.batch):len(s.batch)]
	return update, nil
}

func (s *subscription) accepts(record *containerRecord) bool {
	if s.containersOnly {
		return record.container != nil
	}
	return record.pod != nil
}

// Snapshot capture and queue removal share the publication lock. Conversion
// runs under that lock so each API can reuse its own borrowed result buffer.
func (s *subscription) drain(emit func(PodEventKind, ContainerRef, *Pod)) (PodUpdate, error) {
	v := s.store
	v.mu.Lock()
	defer v.mu.Unlock()
	if s.isClosed {
		return PodUpdate{}, ErrPodSubscriptionClosed
	}
	select {
	case <-s.wake:
	default:
	}
	if v.err != nil && (s.containersOnly || !v.hasSnapshot) {
		return PodUpdate{}, v.err
	}
	update := PodUpdate{DiscoveryError: v.err}
	if s.needsFull {
		for _, record := range v.records {
			if s.accepts(record) {
				emit(PodEventUnknown, record.ref, record.pod)
			}
		}
		clear(s.queue[:])
		s.head, s.count, s.needsFull = 0, 0, false
		update.Revision, update.Mode = v.revision, PodUpdateFull
		return update, nil
	}
	for emitted := 0; s.count > 0 && emitted < containerEventBatchSize; emitted++ {
		item := &s.queue[s.head]
		emit(item.kind, item.ref, item.pod)
		update.Revision = item.revision
		*item = podEvent{}
		s.head = (s.head + 1) % len(s.queue)
		s.count--
	}
	if s.count > 0 {
		s.signal()
	}
	update.Mode = PodUpdateIncremental
	return update, nil
}

// Close does not mutate a previously borrowed result buffer.
func (s *subscription) Close() {
	v := s.store
	v.mu.Lock()
	defer v.mu.Unlock()
	s.closeLocked()
}

func (s *subscription) closeLocked() {
	if s.isClosed {
		return
	}
	s.isClosed = true
	delete(s.store.subscribers, s)
	s.stop()
	clear(s.queue[:])
	s.count = 0
	close(s.wake)
}

func (s *subscription) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (v *containerStore) publish(kind PodEventKind, record *containerRecord) {
	v.revision++
	for s := range v.subscribers {
		if !s.accepts(record) {
			continue
		}
		if !s.needsFull {
			if s.count == len(s.queue) {
				clear(s.queue[:])
				s.head, s.count, s.needsFull = 0, 0, true
			} else {
				s.queue[(s.head+s.count)%len(s.queue)] = podEvent{kind: kind, ref: record.ref, pod: record.pod, revision: v.revision}
				s.count++
			}
		}
		s.signal()
	}
}
