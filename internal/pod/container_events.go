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
type ContainerKey = PodKey

// ContainerRef is an immutable instance description. An empty memory path
// means per-container memory is unavailable or pending. Host Kata VM cgroups
// are deliberately excluded; Guest discovery publishes application bindings.
type ContainerRef struct {
	Key              ContainerKey
	InitPID          int
	MemoryCgroupPath string
}

type ContainerEventKind = PodEventKind

const (
	ContainerEventUnknown = PodEventUnknown
	ContainerCreated      = PodCreated
	ContainerDeleted      = PodDeleted
)

type ContainerEvent struct {
	Kind      ContainerEventKind
	Container ContainerRef
}

// ContainerUpdateMode distinguishes full views from incremental events.
type ContainerUpdateMode = PodUpdateMode

const (
	ContainerUpdateUnknown     = PodUpdateUnknown
	ContainerUpdateFull        = PodUpdateFull
	ContainerUpdateIncremental = PodUpdateIncremental
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

// ContainerSubscription is the legacy projection of the shared lifecycle.
// It excludes sandbox objects and retains the original discovery error contract.
type ContainerSubscription struct {
	subscription
	batch []ContainerEvent
}

func SubscribeContainers(ctx context.Context) (*ContainerSubscription, error) {
	return containerView.subscribe(ctx)
}

func (v *containerStore) subscribe(ctx context.Context) (*ContainerSubscription, error) {
	s := &ContainerSubscription{batch: make([]ContainerEvent, 0, containerEventBatchSize)}
	if err := v.addSubscription(ctx, &s.subscription, true); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *ContainerSubscription) DrainEvents() (ContainerUpdate, error) {
	clear(s.batch)
	s.batch = s.batch[:0]
	update, err := s.drain(func(kind PodEventKind, ref ContainerRef, _ *Pod) {
		s.batch = append(s.batch, ContainerEvent{Kind: kind, Container: ref})
	})
	if err != nil {
		return ContainerUpdate{}, err
	}
	return ContainerUpdate{
		Revision: update.Revision, Mode: update.Mode,
		Events: s.batch[:len(s.batch):len(s.batch)],
	}, nil
}
