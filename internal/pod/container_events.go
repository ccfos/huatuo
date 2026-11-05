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

import (
	"context"
	"errors"
	"slices"
)

const (
	// Overflow replaces queued deltas with one authoritative view, so slow
	// consumers cannot block the producer or retain an unbounded history.
	containerEventQueueSize = 256
	containerEventBatchSize = 64
)

var (
	// ErrContainersUnavailable means the current view cannot establish absence.
	ErrContainersUnavailable = errors.New("container view is unavailable")
	// ErrContainerSubscriptionClosed is terminal; Notify is closed as well.
	ErrContainerSubscriptionClosed = errors.New("container subscription is closed")
	// ErrContainerManagerDisabled means no producer owns the container view.
	ErrContainerManagerDisabled = errors.New("container manager is disabled")
)

// ContainerKey identifies one process and memory cgroup binding. Generations
// increase across manager restarts within one process; they are not persistent.
type ContainerKey struct {
	ID         string
	Generation uint64
}

// ContainerRef is an immutable instance description. An empty memory path
// means per-container memory is unavailable or pending. Host Kata VM cgroups
// are deliberately excluded; Guest discovery publishes application bindings.
type ContainerRef struct {
	Key              ContainerKey
	InitPID          int
	MemoryCgroupPath string
}

type ContainerEventKind uint8

const (
	ContainerEventUnknown ContainerEventKind = iota
	ContainerCreated
	ContainerDeleted
)

type ContainerEvent struct {
	Kind      ContainerEventKind
	Container ContainerRef
}

// ContainerUpdateMode distinguishes full views from incremental events.
type ContainerUpdateMode uint8

const (
	ContainerUpdateUnknown ContainerUpdateMode = iota
	ContainerUpdateFull
	ContainerUpdateIncremental
)

// ContainerUpdate carries a full set (Kind is Unknown and ignored) or ordered
// Created/Deleted events. Full updates reconcile by Container.Key, preserving matches.
// Empty full updates clear the set; empty incremental and zero updates do nothing.
// Revision identifies the full view or last event. Events is read-only until the
// subscription's next DrainEvents call.
type ContainerUpdate struct {
	Revision uint64
	Mode     ContainerUpdateMode
	Events   []ContainerEvent
}

// ContainerSubscription owns one bounded event queue. DrainEvents has a single
// consumer; Close and context cancellation may overlap it. No per-container or
// per-subscription background goroutine is needed while the subscription is live.
type ContainerSubscription struct {
	store     *containerStore
	wake      chan struct{}
	stop      func() bool
	queue     [containerEventQueueSize]containerEvent
	head      int
	count     int
	needsFull bool
	isClosed  bool
	batch     []ContainerEvent
}

type containerEvent struct {
	event    ContainerEvent
	revision uint64
}

// SubscribeContainers attaches to the manager's shared producer. It returns
// immediately, including during initialization: DrainEvents then reports
// ErrContainersUnavailable until a complete view is committed. ctx owns the
// subscription lifetime; cancellation closes it independently of other clients.
func SubscribeContainers(ctx context.Context) (*ContainerSubscription, error) {
	return containerView.subscribe(ctx)
}

func (v *containerStore) subscribe(ctx context.Context) (*ContainerSubscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.isActive {
		return nil, ErrContainerManagerDisabled
	}
	s := &ContainerSubscription{
		store:     v,
		wake:      make(chan struct{}, 1),
		needsFull: true,
		batch:     make([]ContainerEvent, 0, containerEventBatchSize),
	}
	v.subscribers[s] = struct{}{}
	s.stop = context.AfterFunc(ctx, s.Close)
	s.signal()
	return s, nil
}

// Notify is a coalesced readiness signal, including synchronization errors.
// The caller must drain after a wakeup; closure is terminal, not an idle wakeup.
func (s *ContainerSubscription) Notify() <-chan struct{} { return s.wake }

// DrainEvents never waits for input. Returned slices are read-only and borrowed
// until the next DrainEvents call; producers and Close do not mutate them.
// Snapshot capture, covered-event removal and subsequent publication share the
// store lock, leaving no snapshot-to-increment gap. Errors never imply deletion.
func (s *ContainerSubscription) DrainEvents() (ContainerUpdate, error) {
	v := s.store
	v.mu.Lock()
	defer v.mu.Unlock()
	if s.isClosed {
		return ContainerUpdate{}, ErrContainerSubscriptionClosed
	}
	select {
	case <-s.wake:
	default:
	}
	if v.err != nil {
		return ContainerUpdate{}, v.err
	}

	clear(s.batch)
	s.batch = s.batch[:0]
	if s.needsFull {
		s.batch = slices.Grow(s.batch, len(v.records))
		for _, record := range v.records {
			s.batch = append(s.batch, ContainerEvent{Container: record.ref})
		}
		clear(s.queue[:])
		s.head, s.count, s.needsFull = 0, 0, false
		return ContainerUpdate{
			Revision: v.revision, Mode: ContainerUpdateFull,
			Events: s.batch[:len(s.batch):len(s.batch)],
		}, nil
	}

	var revision uint64
	for s.count > 0 && len(s.batch) < containerEventBatchSize {
		item := &s.queue[s.head]
		s.batch = append(s.batch, item.event)
		revision = item.revision
		*item = containerEvent{}
		s.head = (s.head + 1) % len(s.queue)
		s.count--
	}
	if s.count > 0 {
		s.signal()
	}
	return ContainerUpdate{Revision: revision, Mode: ContainerUpdateIncremental, Events: s.batch[:len(s.batch):len(s.batch)]}, nil
}

// Close is idempotent and releases only this subscription. Borrowed batches
// remain immutable; all publication and channel closure serialize on the store.
func (s *ContainerSubscription) Close() {
	v := s.store
	v.mu.Lock()
	defer v.mu.Unlock()
	s.closeLocked()
}

func (s *ContainerSubscription) closeLocked() {
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

func (s *ContainerSubscription) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (v *containerStore) publish(kind ContainerEventKind, ref ContainerRef) {
	v.revision++
	for s := range v.subscribers {
		if !s.needsFull {
			if s.count == len(s.queue) {
				clear(s.queue[:])
				s.head, s.count, s.needsFull = 0, 0, true
			} else {
				s.queue[(s.head+s.count)%len(s.queue)] = containerEvent{
					event: ContainerEvent{Kind: kind, Container: ref}, revision: v.revision,
				}
				s.count++
			}
		}
		s.signal()
	}
}
