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

package autotracing

import (
	"context"
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
)

var (
	errCgroupTrackerClosed        = errors.New("memory cgroup tracker is closed")
	errCgroupRegistrationConflict = errors.New("memory cgroup registration belongs to another container")
)

// memoryEventObservation owns immutable input for the capture worker.
type memoryEventObservation struct {
	Cgroup         cgroupRef
	RegistrationID memoryWatchRegistrationID
	Container      pod.ContainerRef
}

// cgroupUpdate borrows tracker buffers until the next ProcessContainerEvents,
// ProcessMemoryEvents or Close call. Consume each update before the next call.
type cgroupUpdate struct {
	// Invalidated identifies stale capture inputs; their watches may still be active.
	Invalidated []memoryWatchRegistrationID
}

// Admission comes from the scheduler; the tracker has no cooldown policy.
type memoryEventOptions struct {
	AcceptPending bool
}

// cgroupWatchState binds one container to a directory instance and its watch.
// A zero registrationID retains the binding without retrying a failed watch.
type cgroupWatchState struct {
	cgroup         cgroupRef
	container      pod.ContainerRef
	registrationID memoryWatchRegistrationID
}

// cgroupTracker owns registrations and pending candidates on the scheduler
// goroutine. It neither subscribes to pod nor discovers containers.
type cgroupTracker struct {
	watcher   *memoryThresholdWatcher
	directory func(pod.ContainerRef) (os.FileInfo, error)

	containers map[string]*cgroupWatchState
	targets    map[memoryWatchRegistrationID]*cgroupWatchState
	pending    map[memoryWatchRegistrationID]*cgroupWatchState
	batch      cgroupUpdate
	isClosed   bool
}

func newCgroupTracker(watcher *memoryThresholdWatcher) *cgroupTracker {
	return &cgroupTracker{
		watcher: watcher, directory: pod.MemoryCgroupDirectory,
		containers: make(map[string]*cgroupWatchState),
		targets:    make(map[memoryWatchRegistrationID]*cgroupWatchState),
		pending:    make(map[memoryWatchRegistrationID]*cgroupWatchState),
		batch: cgroupUpdate{
			Invalidated: make([]memoryWatchRegistrationID, 0, defaultMemoryWatchEventBatch),
		},
	}
}

// ProcessContainerEvents reconciles a full container view or applies ordered deltas.
// Subscription errors must never become empty full updates.
func (c *cgroupTracker) ProcessContainerEvents(ctx context.Context, containers pod.ContainerUpdate) (cgroupUpdate, error) {
	if c.isClosed {
		return cgroupUpdate{}, errCgroupTrackerClosed
	}
	if err := ctx.Err(); err != nil {
		return cgroupUpdate{}, err
	}
	c.batch.Invalidated = c.batch.Invalidated[:0]
	switch containers.Mode {
	case pod.ContainerUpdateFull:
		// Authoritative full views contain unique keys. Unchanged views need
		// neither temporary indexes nor another directory lookup.
		unchanged := len(containers.Events) == len(c.containers)
		if unchanged {
			for i := range containers.Events {
				key := containers.Events[i].Container.Key
				if entry := c.containers[key.ID]; entry == nil || entry.container.Key != key {
					unchanged = false
					break
				}
			}
		}
		if unchanged {
			break
		}

		desired := make(map[string]struct{}, len(containers.Events))
		for i := range containers.Events {
			desired[containers.Events[i].Container.Key.ID] = struct{}{}
		}
		// Release departed containers before admitting replacements at capacity.
		for id, entry := range c.containers {
			if _, exists := desired[id]; !exists {
				if err := c.deleteContainer(ctx, entry.container.Key); err != nil {
					return cgroupUpdate{}, err
				}
			}
		}
		for i := range containers.Events {
			if err := c.createContainer(ctx, containers.Events[i].Container); err != nil {
				return cgroupUpdate{}, err
			}
		}
	case pod.ContainerUpdateIncremental:
		for i := range containers.Events {
			event := &containers.Events[i]
			var err error
			switch event.Kind {
			case pod.ContainerCreated:
				err = c.createContainer(ctx, event.Container)
			case pod.ContainerDeleted:
				err = c.deleteContainer(ctx, event.Container.Key)
			}
			if err != nil {
				return cgroupUpdate{}, err
			}
		}
	}
	return cgroupUpdate{
		Invalidated: c.batch.Invalidated[:len(c.batch.Invalidated):len(c.batch.Invalidated)],
	}, nil
}

// ProcessMemoryEvents admits observations only for active registrations.
// Process container changes first so retired registrations cannot admit pressure.
// Disabling admission still processes invalidations and terminal errors.
func (c *cgroupTracker) ProcessMemoryEvents(ctx context.Context, events []memoryWatchEvent, options memoryEventOptions) (cgroupUpdate, error) {
	if c.isClosed {
		return cgroupUpdate{}, errCgroupTrackerClosed
	}
	if err := ctx.Err(); err != nil {
		return cgroupUpdate{}, err
	}
	c.batch.Invalidated = c.batch.Invalidated[:0]
	for i := range events {
		event := &events[i]
		entry := c.targets[event.TargetID]
		if entry == nil {
			continue
		}
		switch event.Kind {
		case memoryThresholdObserved:
			if options.AcceptPending {
				c.pending[event.TargetID] = entry
			}
		case memoryTargetRemoved, memoryTargetUnavailable:
			if isResourceExhaustion(event.Err) || errors.Is(event.Err, errMemoryWatchEventOverflow) {
				return cgroupUpdate{}, event.Err
			}
			if errors.Is(event.Err, errMemoryWatchLimitUnavailable) {
				continue
			}
			if err := c.unregister(ctx, entry); err != nil {
				return cgroupUpdate{}, err
			}
			log.WithField("cgroup", entry.cgroup.Path).WithError(event.Err).Warn("memory watch target unavailable; skipping cgroup instance")
		}
	}
	return cgroupUpdate{
		Invalidated: c.batch.Invalidated[:len(c.batch.Invalidated):len(c.batch.Invalidated)],
	}, nil
}

// PendingCount counts distinct registrations, not repeated notifications.
func (c *cgroupTracker) PendingCount() int {
	return len(c.pending)
}

// DrainPending transfers all candidates to the caller. Later tracker calls
// cannot mutate this batch while a capture worker is using it.
func (c *cgroupTracker) DrainPending() []memoryEventObservation {
	if len(c.pending) == 0 {
		return nil
	}

	observations := make([]memoryEventObservation, 0, len(c.pending))
	for id, state := range c.pending {
		observations = append(observations, memoryEventObservation{
			Cgroup: state.cgroup, RegistrationID: id,
			Container: state.container,
		})
	}
	c.ClearPending()
	return observations
}

// ClearPending discards candidates without changing their registrations.
func (c *cgroupTracker) ClearPending() {
	clear(c.pending)
}

// Close releases metadata. The owner closes the dedicated watcher once, after
// joining capture work, to release all native registrations in one shutdown.
func (c *cgroupTracker) Close() {
	c.isClosed = true
	c.ClearPending()
	clear(c.containers)
	clear(c.targets)
}

func (c *cgroupTracker) createContainer(ctx context.Context, ref pod.ContainerRef) error {
	old := c.containers[ref.Key.ID]
	if old != nil && old.container.Key.Generation >= ref.Key.Generation {
		return nil
	}

	err := c.bindContainer(ctx, ref, old)
	if err != nil {
		if ctx.Err() != nil || isResourceExhaustion(err) || errors.Is(err, errMemoryWatchClosed) ||
			errors.Is(err, errMemoryWatchEventOverflow) || errors.Is(err, errCgroupRegistrationConflict) {
			return err
		}
		log.WithField("container", ref.Key.ID).WithError(err).Warn("memory watch registration failed; skipping container instance")
	}

	return nil
}

func (c *cgroupTracker) bindContainer(ctx context.Context, ref pod.ContainerRef, old *cgroupWatchState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entry := &cgroupWatchState{container: ref, cgroup: cgroupRef{Path: ref.MemoryCgroupPath}}
	var err error
	if entry.cgroup.Path != "" {
		entry.cgroup.directory, err = c.directory(ref)
	}
	if old != nil && err == nil && old.cgroup.SameInstance(entry.cgroup) {
		old.container = ref
		if old.registrationID != 0 {
			c.batch.Invalidated = append(c.batch.Invalidated, old.registrationID)
		}
		return nil
	}
	if old != nil {
		if deleteErr := c.deleteContainer(ctx, old.container.Key); deleteErr != nil {
			return errors.Join(err, deleteErr)
		}
	}
	c.containers[ref.Key.ID] = entry
	if err != nil || entry.cgroup.Path == "" {
		return err
	}

	// The watcher validates directory identity and reports replaced registrations
	// through TargetRemoved; no path index is needed here.
	return c.register(ctx, entry)
}

func (c *cgroupTracker) deleteContainer(ctx context.Context, key pod.ContainerKey) error {
	entry := c.containers[key.ID]
	if entry == nil || entry.container.Key != key {
		return nil
	}
	if err := c.unregister(ctx, entry); err != nil {
		return err
	}
	delete(c.containers, key.ID)

	return nil
}

func (c *cgroupTracker) register(ctx context.Context, state *cgroupWatchState) error {
	id, err := c.watcher.Register(ctx, state.cgroup.Path, state.cgroup.directory)
	if err != nil {
		return err
	}
	// Native registration is idempotent; it must not transfer container ownership.
	if owner := c.targets[id]; owner != nil {
		return fmt.Errorf("%w: cgroup %q, containers %q and %q; configure a separate cgroup for each container",
			errCgroupRegistrationConflict, state.cgroup.Path, owner.container.Key.ID, state.container.Key.ID)
	}
	state.registrationID = id
	c.targets[id] = state

	return nil
}

func (c *cgroupTracker) unregister(ctx context.Context, state *cgroupWatchState) error {
	id := state.registrationID
	if id == 0 {
		return nil
	}
	if err := c.watcher.Unregister(ctx, id); err != nil {
		return err
	}
	delete(c.targets, id)
	delete(c.pending, id)
	state.registrationID = 0
	c.batch.Invalidated = append(c.batch.Invalidated, id)

	return nil
}

func isResourceExhaustion(err error) bool {
	return errors.Is(err, unix.EMFILE) || errors.Is(err, unix.ENFILE) || errors.Is(err, unix.ENOSPC) || errors.Is(err, unix.ENOMEM)
}
