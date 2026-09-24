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
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/cgroups/memorywatch"
	"github.com/ccfos/huatuo/internal/pod"
)

func TestCgroupTrackerResourceExhaustion(t *testing.T) {
	for _, test := range []struct {
		name  string
		cause error
		want  bool
	}{
		{name: "nil"},
		{name: "permission", cause: os.ErrPermission},
		{name: "closed", cause: memorywatch.ErrClosed},
		{name: "target limit", cause: memorywatch.ErrTargetLimit},
		{name: "event overflow", cause: memorywatch.ErrEventOverflow},
		{name: "canceled", cause: context.Canceled},
		{name: "process fd limit", cause: unix.EMFILE, want: true},
		{name: "system fd limit", cause: unix.ENFILE, want: true},
		{name: "inotify capacity", cause: unix.ENOSPC, want: true},
		{name: "memory allocation", cause: unix.ENOMEM, want: true},
		{name: "wrapped failure", cause: &os.PathError{Op: "open", Path: "/cgroup", Err: unix.EMFILE}, want: true},
		{name: "joined failure", cause: errors.Join(context.Canceled, unix.ENOSPC), want: true},
		{name: "limit and resource failure", cause: errors.Join(memorywatch.ErrTargetLimit, unix.EMFILE), want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := isResourceExhaustion(test.cause); got != test.want {
				t.Fatalf("resource exhaustion for %v = %t, want %t", test.cause, got, test.want)
			}
		})
	}
}

func TestCgroupTrackerPressureBatchBudget(t *testing.T) {
	tracker, source := newTestCgroupTracker(t)
	const count = 2*defaultMemoryWatchEventBatch + 1
	for i := 1; i <= count; i++ {
		path := fmt.Sprintf("/%064x", i)
		createMemoryCgroupForTest(t, source.root, path, 95)
		if err := addMemoryCgroupForTest(t, tracker, path); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for tracker.PendingCount() < count {
		select {
		case <-tracker.watcher.Notifications():
		case <-ctx.Done():
			t.Fatal("remaining pressure events lost their notification")
		}
		before := tracker.PendingCount()
		update, err := processCgroupEventsForTest(tracker, ctx)
		if err != nil {
			t.Fatal(err)
		}
		if tracker.PendingCount()-before > defaultMemoryWatchEventBatch || len(update.Invalidated) != 0 {
			t.Fatal("native event processing exceeded its batch budget or invalidated a target")
		}
	}
	batch := tracker.DrainPending()
	if len(batch) != count || tracker.PendingCount() != 0 {
		t.Fatalf("pending drain returned %d of %d targets", len(batch), count)
	}
	seen := make(map[memoryWatchRegistrationID]bool)
	for i := range batch {
		id := batch[i].RegistrationID
		if seen[id] {
			t.Fatalf("duplicate pending registration %d", id)
		}
		seen[id] = true
	}
}

func TestCgroupTrackerPendingAdmissionAndOwnership(t *testing.T) {
	tracker, source := newTestCgroupTracker(t)
	if tracker.PendingCount() != 0 || tracker.DrainPending() != nil {
		t.Fatal("new tracker returned pending targets")
	}
	first, second := "/"+strings.Repeat("a", 64), "/"+strings.Repeat("b", 64)
	for _, path := range []string{first, second} {
		createMemoryCgroupForTest(t, source.root, path, 1)
		if err := addMemoryCgroupForTest(t, tracker, path); err != nil {
			t.Fatal(err)
		}
	}
	firstID := tracker.containers[containerRefForTest(first).Key.ID].registrationID
	secondID := tracker.containers[containerRefForTest(second).Key.ID].registrationID
	pressure := []memoryWatchEvent{
		{Kind: memoryThresholdObserved, TargetID: firstID},
		{Kind: memoryThresholdObserved, TargetID: firstID},
		{Kind: memoryThresholdObserved},
	}
	if _, err := tracker.ProcessMemoryEvents(t.Context(), pressure, memoryEventOptions{AcceptPending: true}); err != nil {
		t.Fatal(err)
	}
	if tracker.PendingCount() != 1 {
		t.Fatal("duplicate or unregistered notification changed the pending count")
	}
	if _, err := tracker.ProcessMemoryEvents(t.Context(), []memoryWatchEvent{
		{Kind: memoryThresholdObserved, TargetID: secondID},
	}, memoryEventOptions{}); err != nil {
		t.Fatal(err)
	}
	batch := tracker.DrainPending()
	if len(batch) != 1 || batch[0].RegistrationID != firstID || batch[0].Cgroup.Path != first || tracker.PendingCount() != 0 {
		t.Fatal("disabled admission changed the existing candidates or drain lost their identity")
	}
	retained := slices.Clone(batch)
	if _, err := tracker.ProcessMemoryEvents(t.Context(), pressure, memoryEventOptions{AcceptPending: true}); err != nil {
		t.Fatal(err)
	}
	tracker.ClearPending()
	if tracker.PendingCount() != 0 || tracker.DrainPending() != nil || !reflect.DeepEqual(batch, retained) {
		t.Fatal("clearing pending targets changed an owned batch")
	}
	batch[0].Cgroup.Path = "/caller-owned"
	if _, err := tracker.ProcessMemoryEvents(t.Context(), pressure, memoryEventOptions{AcceptPending: true}); err != nil {
		t.Fatal(err)
	}
	next := tracker.DrainPending()
	if !reflect.DeepEqual(next, retained) {
		t.Fatal("caller mutation or ClearPending changed the registered target")
	}
	if _, err := tracker.ProcessMemoryEvents(t.Context(), pressure, memoryEventOptions{AcceptPending: true}); err != nil {
		t.Fatal(err)
	}
	tracker.Close()
	if tracker.PendingCount() != 0 || tracker.DrainPending() != nil || !reflect.DeepEqual(next, retained) {
		t.Fatal("closing the tracker retained candidates or changed an owned batch")
	}
	if len(tracker.containers) != 0 || len(tracker.targets) != 0 {
		t.Fatal("closing the tracker retained container ownership or registrations")
	}
}

func TestCgroupTrackerPendingRemovalOrdering(t *testing.T) {
	tracker, source := newTestCgroupTracker(t)
	path := "/" + strings.Repeat("a", 64)
	createMemoryCgroupForTest(t, source.root, path, 1)
	if err := addMemoryCgroupForTest(t, tracker, path); err != nil {
		t.Fatal(err)
	}
	id := tracker.containers[containerRefForTest(path).Key.ID].registrationID
	update, err := tracker.ProcessMemoryEvents(t.Context(), []memoryWatchEvent{
		{Kind: memoryThresholdObserved, TargetID: id},
		{Kind: memoryTargetRemoved, TargetID: id},
		{Kind: memoryThresholdObserved, TargetID: id},
	}, memoryEventOptions{AcceptPending: true})
	if err != nil || !slices.Equal(update.Invalidated, []memoryWatchRegistrationID{id}) || tracker.PendingCount() != 0 {
		t.Fatalf("removal left a stale candidate: %+v, %v", update, err)
	}
}

func newTestCgroupTracker(t testing.TB) (*cgroupTracker, *cgroupSource) {
	t.Helper()
	source := newTestCgroupSource(t)
	watcher, err := newMemoryThresholdWatcher(t.Context(), memoryWatchOptions{ThresholdPercent: 90})
	if err != nil {
		t.Fatal(err)
	}
	tracker := fixtureCgroupTracker(source, watcher)
	t.Cleanup(func() {
		tracker.Close()
		if err := watcher.Close(); err != nil {
			t.Error(err)
		}
	})
	return tracker, source
}

func processCgroupEventsForTest(tracker *cgroupTracker, ctx context.Context) (cgroupUpdate, error) {
	events, err := tracker.watcher.ProcessEvents(ctx)
	if err != nil {
		return cgroupUpdate{}, err
	}
	return tracker.ProcessMemoryEvents(ctx, events, memoryEventOptions{AcceptPending: true})
}

func addMemoryCgroupForTest(t testing.TB, tracker *cgroupTracker, path string) error {
	t.Helper()
	return tracker.createContainer(t.Context(), containerRefForTest(path))
}

func applyContainerEventsForTest(t *testing.T, tracker *cgroupTracker, events ...pod.ContainerEvent) cgroupUpdate {
	t.Helper()
	update, err := tracker.ProcessContainerEvents(t.Context(), pod.ContainerUpdate{Mode: pod.ContainerUpdateIncremental, Events: events})
	if err != nil {
		t.Fatal(err)
	}
	return update
}

func waitMemoryPressureForTest(t *testing.T, tracker *cgroupTracker, path string) memoryEventObservation {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-tracker.watcher.Notifications():
		}
		_, err := processCgroupEventsForTest(tracker, ctx)
		if err != nil {
			t.Fatal(err)
		}
		batch := tracker.DrainPending()
		for i := range batch {
			if batch[i].Cgroup.Path == path {
				return batch[i]
			}
		}
	}
}

func waitMemoryInvalidationForTest(t *testing.T, tracker *cgroupTracker, id memoryWatchRegistrationID) cgroupUpdate {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("replaced registration did not notify the tracker")
		case <-tracker.watcher.Notifications():
		}
		update, err := processCgroupEventsForTest(tracker, ctx)
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(update.Invalidated, id) {
			return update
		}
	}
}

func TestCgroupTrackerReplacementAndLateDelete(t *testing.T) {
	tracker, source := newTestCgroupTracker(t)
	path := "/" + strings.Repeat("a", 64)
	createMemoryCgroupForTest(t, source.root, path, 95)
	ref := containerRefForTest(path)
	applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerCreated, Container: ref})
	old := tracker.containers[ref.Key.ID].registrationID
	if _, err := tracker.ProcessMemoryEvents(t.Context(), []memoryWatchEvent{
		{Kind: memoryThresholdObserved, TargetID: old},
	}, memoryEventOptions{AcceptPending: true}); err != nil {
		t.Fatal(err)
	}
	applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerCreated, Container: ref})
	if tracker.containers[ref.Key.ID].registrationID != old {
		t.Fatal("duplicate create replaced registration")
	}
	if err := os.Rename(source.memcgDir(path), filepath.Join(source.root, "old")); err != nil {
		t.Fatal(err)
	}
	createMemoryCgroupForTest(t, source.root, path, 95)
	replacement := ref
	replacement.Key.Generation++
	update := applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerCreated, Container: replacement})
	if !slices.Equal(update.Invalidated, []memoryWatchRegistrationID{old}) || tracker.PendingCount() != 0 {
		t.Fatalf("invalidated = %v", update.Invalidated)
	}
	id := tracker.containers[ref.Key.ID].registrationID
	if id == old {
		t.Fatal("replacement reused target ID")
	}
	if observation := waitMemoryPressureForTest(t, tracker, path); observation.RegistrationID != id {
		t.Fatal("replacement notification did not carry the new registration")
	}
	update = applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerDeleted, Container: ref})
	if len(update.Invalidated) != 0 || tracker.containers[ref.Key.ID].registrationID != id {
		t.Fatal("late delete retired replacement")
	}
	update, err := tracker.ProcessMemoryEvents(t.Context(), []memoryWatchEvent{{Kind: memoryThresholdObserved, TargetID: old}}, memoryEventOptions{AcceptPending: true})
	if err != nil || len(update.Invalidated) != 0 || tracker.PendingCount() != 0 {
		t.Fatal("retired pressure was admitted")
	}
	if _, err := tracker.ProcessMemoryEvents(t.Context(), []memoryWatchEvent{
		{Kind: memoryThresholdObserved, TargetID: id},
	}, memoryEventOptions{AcceptPending: true}); err != nil {
		t.Fatal(err)
	}
	update = applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerDeleted, Container: replacement})
	if !slices.Equal(update.Invalidated, []memoryWatchRegistrationID{id}) || tracker.PendingCount() != 0 {
		t.Fatal("container deletion retained the pending registration")
	}
}

func TestCgroupTrackerFullUpdateAndGenerationChange(t *testing.T) {
	tracker, source := newTestCgroupTracker(t)
	path := "/container"
	createMemoryCgroupForTest(t, source.root, path, 95)
	ref := containerRefForTest(path)
	applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerCreated, Container: ref})
	state := tracker.containers[ref.Key.ID]
	id := state.registrationID
	pressure := []memoryWatchEvent{{Kind: memoryThresholdObserved, TargetID: id}}
	if _, err := tracker.ProcessMemoryEvents(t.Context(), pressure, memoryEventOptions{AcceptPending: true}); err != nil {
		t.Fatal(err)
	}
	oldBatch := tracker.DrainPending()
	replacement := ref
	replacement.Key.Generation++
	full := pod.ContainerUpdate{Mode: pod.ContainerUpdateFull, Events: []pod.ContainerEvent{{Container: replacement}}}
	update, err := tracker.ProcessContainerEvents(t.Context(), full)
	if err != nil || !slices.Equal(update.Invalidated, []memoryWatchRegistrationID{id}) ||
		state.registrationID != id || state.container != replacement || len(tracker.targets) != 1 {
		t.Fatalf("generation replacement = %+v, %v", update, err)
	}
	if len(oldBatch) != 1 || oldBatch[0].Container != ref {
		t.Fatal("generation replacement mutated a submitted observation")
	}
	update = applyContainerEventsForTest(
		t, tracker,
		pod.ContainerEvent{Kind: pod.ContainerCreated, Container: ref},
		pod.ContainerEvent{Kind: pod.ContainerDeleted, Container: ref},
	)
	if len(update.Invalidated) != 0 || state.container != replacement || state.registrationID != id {
		t.Fatal("stale lifecycle events changed the current binding")
	}
	update, err = tracker.ProcessContainerEvents(t.Context(), full)
	if err != nil || len(update.Invalidated) != 0 {
		t.Fatalf("duplicate full view = %+v, %v", update, err)
	}
	if _, err := tracker.ProcessMemoryEvents(t.Context(), pressure, memoryEventOptions{AcceptPending: true}); err != nil {
		t.Fatal(err)
	}
	batch := tracker.DrainPending()
	if len(batch) != 1 || batch[0].RegistrationID != id || batch[0].Container != replacement {
		t.Fatalf("current container observation = %+v", batch)
	}
	if _, err := tracker.ProcessContainerEvents(t.Context(), pod.ContainerUpdate{Mode: pod.ContainerUpdateFull}); err != nil {
		t.Fatal(err)
	}
	if len(tracker.containers) != 0 || len(tracker.targets) != 0 {
		t.Fatal("empty full view retained bindings")
	}
}

func TestCgroupTrackerFullViewReplacesContainerBinding(t *testing.T) {
	tracker, source := newTestCgroupTracker(t)
	path := "/container"
	createMemoryCgroupForTest(t, source.root, path, 1)
	first := containerRefForTest(path)
	applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerCreated, Container: first})
	id := tracker.containers[first.Key.ID].registrationID
	second := first
	second.Key.ID = "replacement"
	update, err := tracker.ProcessContainerEvents(t.Context(), pod.ContainerUpdate{
		Mode: pod.ContainerUpdateFull, Events: []pod.ContainerEvent{{Container: second}},
	})
	if err != nil || !slices.Equal(update.Invalidated, []memoryWatchRegistrationID{id}) {
		t.Fatalf("full view replacement = %+v, %v", update, err)
	}
	state := tracker.containers[second.Key.ID]
	if state.registrationID == 0 || state.registrationID == id || state.container != second || len(tracker.containers) != 1 {
		t.Fatal("full view did not replace the container binding")
	}
}

func TestCgroupTrackerPendingMemoryBinding(t *testing.T) {
	tracker, source := newTestCgroupTracker(t)
	path := "/" + strings.Repeat("a", 64)
	createMemoryCgroupForTest(t, source.root, path, 95)
	pending := containerRefForTest(path)
	pending.MemoryCgroupPath = ""
	applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerCreated, Container: pending})
	if len(tracker.containers) != 1 || len(tracker.targets) != 0 {
		t.Fatal("pending memory binding acquired a registration")
	}
	current := pending
	current.Key.Generation++
	current.MemoryCgroupPath = path
	applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerCreated, Container: current})
	entry := tracker.containers[current.Key.ID]
	if entry == nil || entry.container != current {
		t.Fatal("resolved memory binding did not register")
	}
	id := entry.registrationID
	applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerDeleted, Container: pending})
	if tracker.containers[current.Key.ID] != entry || entry.registrationID != id {
		t.Fatal("stale pending binding deletion retired the resolved binding")
	}
	applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerDeleted, Container: current})
	if len(tracker.containers) != 0 || len(tracker.targets) != 0 {
		t.Fatal("resolved binding deletion retained ownership")
	}
}

func TestCgroupTrackerEmptyUpdateModes(t *testing.T) {
	for _, test := range []struct {
		name    string
		mode    pod.ContainerUpdateMode
		removes bool
	}{
		{name: "zero value", mode: pod.ContainerUpdateUnknown},
		{name: "incremental", mode: pod.ContainerUpdateIncremental},
		{name: "full", mode: pod.ContainerUpdateFull, removes: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			tracker, source := newTestCgroupTracker(t)
			path := "/" + strings.Repeat("a", 64)
			createMemoryCgroupForTest(t, source.root, path, 1)
			applyContainerEventsForTest(t, tracker, pod.ContainerEvent{
				Kind: pod.ContainerCreated, Container: containerRefForTest(path),
			})
			id := tracker.containers[containerRefForTest(path).Key.ID].registrationID
			pressure := []memoryWatchEvent{{Kind: memoryThresholdObserved, TargetID: id}}
			if _, err := tracker.ProcessMemoryEvents(t.Context(), pressure, memoryEventOptions{AcceptPending: true}); err != nil {
				t.Fatal(err)
			}
			update, err := tracker.ProcessContainerEvents(t.Context(), pod.ContainerUpdate{Mode: test.mode})
			if err != nil {
				t.Fatal(err)
			}
			if test.removes {
				if !slices.Equal(update.Invalidated, []memoryWatchRegistrationID{id}) || tracker.PendingCount() != 0 {
					t.Fatal("empty full update did not retire the target before pressure admission")
				}
			} else if len(update.Invalidated) != 0 {
				t.Fatal("empty update changed the registration")
			}
			update, err = tracker.ProcessMemoryEvents(t.Context(), pressure, memoryEventOptions{AcceptPending: true})
			if err != nil || len(update.Invalidated) != 0 {
				t.Fatalf("pressure processing invalidated registrations: %+v, %v", update, err)
			}
			if test.removes {
				if tracker.PendingCount() != 0 {
					t.Fatal("retired pressure was admitted after the empty full update")
				}
			} else {
				batch := tracker.DrainPending()
				if len(batch) != 1 || batch[0].RegistrationID != id {
					t.Fatal("empty update suppressed or duplicated the memory event")
				}
			}
		})
	}
}

func TestCgroupTrackerRegistrationFailureDoesNotRetry(t *testing.T) {
	tracker, source := newTestCgroupTracker(t)
	path := "/failed"
	ref := containerRefForTest(path)
	createMemoryCgroupForTest(t, source.root, path, 95)
	for _, name := range []string{"memory.max", "memory.limit_in_bytes"} {
		if err := os.Remove(filepath.Join(source.memcgDir(path), name)); err != nil {
			t.Fatal(err)
		}
	}
	state := &cgroupWatchState{cgroup: cgroupRefForTest(t, source, path)}
	if err := tracker.register(t.Context(), state); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("registration failure = %v", err)
	}
	if state.registrationID != 0 || len(tracker.targets) != 0 {
		t.Fatal("failed registration published target")
	}
	applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerCreated, Container: ref})
	state = tracker.containers[ref.Key.ID]
	if state == nil || state.container != ref || state.registrationID != 0 {
		t.Fatal("failure lost container binding")
	}
	createMemoryCgroupForTest(t, source.root, path, 95)
	ref.Key.Generation++
	applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerCreated, Container: ref})
	if _, err := tracker.ProcessContainerEvents(t.Context(), pod.ContainerUpdate{
		Mode:   pod.ContainerUpdateFull,
		Events: []pod.ContainerEvent{{Container: ref}},
	}); err != nil {
		t.Fatal(err)
	}
	if tracker.containers[ref.Key.ID] != state || state.registrationID != 0 || state.container != ref || len(tracker.targets) != 0 {
		t.Fatal("generation change retried failed directory watch")
	}
	if err := os.Rename(source.memcgDir(path), filepath.Join(source.root, "old")); err != nil {
		t.Fatal(err)
	}
	createMemoryCgroupForTest(t, source.root, path, 95)
	ref.Key.Generation++
	applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerCreated, Container: ref})
	if tracker.containers[ref.Key.ID] == state || tracker.containers[ref.Key.ID].registrationID == 0 {
		t.Fatal("new directory did not register")
	}
}

func TestCgroupTrackerWatchFailureDoesNotRecover(t *testing.T) {
	for _, test := range []struct {
		name  string
		event memoryWatchEvent
	}{
		{name: "removed", event: memoryWatchEvent{Kind: memoryTargetRemoved}},
		{name: "unavailable", event: memoryWatchEvent{Kind: memoryTargetUnavailable, Err: errors.New("memory usage unavailable")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			tracker, source := newTestCgroupTracker(t)
			path := "/" + strings.Repeat("a", 64)
			createMemoryCgroupForTest(t, source.root, path, 95)
			ref := containerRefForTest(path)
			applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerCreated, Container: ref})
			id := tracker.containers[ref.Key.ID].registrationID
			test.event.TargetID = id
			if _, err := tracker.ProcessMemoryEvents(t.Context(), []memoryWatchEvent{
				{Kind: memoryThresholdObserved, TargetID: id},
			}, memoryEventOptions{AcceptPending: true}); err != nil {
				t.Fatal(err)
			}
			update, err := tracker.ProcessMemoryEvents(t.Context(), []memoryWatchEvent{test.event}, memoryEventOptions{})
			if err != nil || !slices.Equal(update.Invalidated, []memoryWatchRegistrationID{id}) || len(tracker.targets) != 0 ||
				tracker.PendingCount() != 0 || tracker.containers[ref.Key.ID] == nil ||
				tracker.containers[ref.Key.ID].container != ref || tracker.containers[ref.Key.ID].registrationID != 0 {
				t.Fatalf("watch failure did not retire only the registration: %+v, %v", update, err)
			}

			full := pod.ContainerUpdate{Mode: pod.ContainerUpdateFull, Events: []pod.ContainerEvent{{Container: ref}}}
			if _, err := tracker.ProcessContainerEvents(t.Context(), full); err != nil {
				t.Fatal(err)
			}
			update, err = tracker.ProcessMemoryEvents(t.Context(), []memoryWatchEvent{{Kind: memoryThresholdObserved, TargetID: id}}, memoryEventOptions{AcceptPending: true})
			if err != nil || len(update.Invalidated) != 0 || tracker.PendingCount() != 0 || len(tracker.targets) != 0 {
				t.Fatal("full update or stale pressure revived an invalidated registration")
			}
			ref.Key.Generation++
			applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerCreated, Container: ref})
			if tracker.containers[ref.Key.ID].registrationID != 0 {
				t.Fatal("container generation revived a failed directory watch")
			}
			if err := os.Rename(source.memcgDir(path), filepath.Join(source.root, "old")); err != nil {
				t.Fatal(err)
			}
			createMemoryCgroupForTest(t, source.root, path, 95)
			ref.Key.Generation++
			applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerCreated, Container: ref})
			if tracker.containers[ref.Key.ID].registrationID == 0 || tracker.containers[ref.Key.ID].registrationID == id {
				t.Fatal("new directory did not register")
			}
		})
	}
}

func TestCgroupTrackerCancellationAndClose(t *testing.T) {
	tracker, _ := newTestCgroupTracker(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := tracker.ProcessContainerEvents(ctx, pod.ContainerUpdate{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := tracker.ProcessMemoryEvents(ctx, nil, memoryEventOptions{AcceptPending: true}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	tracker.Close()
	tracker.Close()
	if _, err := tracker.ProcessContainerEvents(t.Context(), pod.ContainerUpdate{}); !errors.Is(err, errCgroupTrackerClosed) {
		t.Fatal(err)
	}
	if _, err := tracker.ProcessMemoryEvents(t.Context(), nil, memoryEventOptions{AcceptPending: true}); !errors.Is(err, errCgroupTrackerClosed) {
		t.Fatal(err)
	}
}

func TestCgroupTrackerCapacityLimitDoesNotRecover(t *testing.T) {
	source := newTestCgroupSource(t)
	watcher, err := newMemoryThresholdWatcher(t.Context(), memoryWatchOptions{ThresholdPercent: 90, MaxCgroups: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()
	tracker := fixtureCgroupTracker(source, watcher)
	defer tracker.Close()
	first, second := "/"+strings.Repeat("a", 64), "/"+strings.Repeat("b", 64)
	for _, path := range []string{first, second} {
		createMemoryCgroupForTest(t, source.root, path, 95)
	}
	if err := addMemoryCgroupForTest(t, tracker, first); err != nil {
		t.Fatal(err)
	}
	entry := &cgroupWatchState{cgroup: cgroupRefForTest(t, source, second)}
	if err := tracker.register(t.Context(), entry); !errors.Is(err, errMemoryWatchLimit) {
		t.Fatalf("capacity registration error = %v, want %v", err, errMemoryWatchLimit)
	}
	if entry.registrationID != 0 || len(tracker.targets) != 1 || len(tracker.containers) != 1 {
		t.Fatal("capacity failure changed active registrations")
	}
	if err := addMemoryCgroupForTest(t, tracker, second); err != nil {
		t.Fatal(err)
	}
	if tracker.containers[containerRefForTest(first).Key.ID].registrationID == 0 || tracker.containers[containerRefForTest(second).Key.ID].registrationID != 0 || len(tracker.containers) != 2 ||
		tracker.containers[containerRefForTest(first).Key.ID].container != containerRefForTest(first) || tracker.containers[containerRefForTest(second).Key.ID].container != containerRefForTest(second) {
		t.Fatal("capacity failure did not skip only the second instance")
	}
	pressure := waitMemoryPressureForTest(t, tracker, first)
	if pressure.RegistrationID != tracker.containers[containerRefForTest(first).Key.ID].registrationID {
		t.Fatal("capacity failure interrupted the existing registration")
	}

	applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerDeleted, Container: containerRefForTest(first)})
	ref := containerRefForTest(second)
	full := pod.ContainerUpdate{Mode: pod.ContainerUpdateFull, Events: []pod.ContainerEvent{{Container: ref}}}
	if _, err := tracker.ProcessContainerEvents(t.Context(), full); err != nil {
		t.Fatal(err)
	}
	if len(tracker.targets) != 0 || len(tracker.containers) != 1 || tracker.containers[containerRefForTest(second).Key.ID].container != ref || tracker.containers[containerRefForTest(second).Key.ID].registrationID != 0 {
		t.Fatal("released capacity or full update retried the skipped instance")
	}

	ref.Key.Generation++
	applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerCreated, Container: ref})
	if tracker.containers[containerRefForTest(second).Key.ID].registrationID != 0 {
		t.Fatal("new container generation retried the failed watch")
	}
	applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerDeleted, Container: ref})
	ref.Key.Generation++
	applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerCreated, Container: ref})
	if tracker.containers[containerRefForTest(second).Key.ID].registrationID == 0 {
		t.Fatal("fresh tracking lifetime could not use released capacity")
	}
}

func TestCgroupTrackerCreateContainerTerminalErrors(t *testing.T) {
	for _, test := range []struct {
		name   string
		cancel bool
		want   error
	}{
		{name: "canceled", cancel: true, want: context.Canceled},
		{name: "watcher closed", want: errMemoryWatchClosed},
	} {
		t.Run(test.name, func(t *testing.T) {
			tracker, source := newTestCgroupTracker(t)
			path := "/" + strings.Repeat("a", 64)
			createMemoryCgroupForTest(t, source.root, path, 95)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if test.cancel {
				cancel()
			} else if err := tracker.watcher.Close(); err != nil {
				t.Fatal(err)
			}

			if err := tracker.createContainer(ctx, containerRefForTest(path)); !errors.Is(err, test.want) {
				t.Fatalf("container registration error = %v, want %v", err, test.want)
			}
			if len(tracker.targets) != 0 || (tracker.containers[containerRefForTest(path).Key.ID] != nil && tracker.containers[containerRefForTest(path).Key.ID].registrationID != 0) {
				t.Fatal("terminal registration failure published an active target")
			}
		})
	}
}

func TestCgroupTrackerWatchResourceFailure(t *testing.T) {
	for _, cause := range []error{unix.EMFILE, errMemoryWatchEventOverflow} {
		t.Run(cause.Error(), func(t *testing.T) {
			tracker, source := newTestCgroupTracker(t)
			path := "/" + strings.Repeat("a", 64)
			createMemoryCgroupForTest(t, source.root, path, 1)
			if err := addMemoryCgroupForTest(t, tracker, path); err != nil {
				t.Fatal(err)
			}
			id := tracker.containers[containerRefForTest(path).Key.ID].registrationID
			_, err := tracker.ProcessMemoryEvents(t.Context(), []memoryWatchEvent{
				{Kind: memoryTargetUnavailable, TargetID: id, Err: cause},
			}, memoryEventOptions{})
			if !errors.Is(err, cause) {
				t.Fatalf("resource failure was swallowed: %v", err)
			}
		})
	}
}

func TestCgroupTrackerDirectoryReplacementUsesNativeInvalidation(t *testing.T) {
	tracker, source := newTestCgroupTracker(t)
	path := "/container"
	createMemoryCgroupForTest(t, source.root, path, 95)
	oldRef := containerRefForTest(path)
	oldDirectory := cgroupRefForTest(t, source, path).directory
	applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerCreated, Container: oldRef})
	oldState := tracker.containers[oldRef.Key.ID]
	oldID := oldState.registrationID
	// The scheduler may have borrowed pressure before the lifecycle update.
	pressure, err := tracker.watcher.ProcessEvents(t.Context())
	if err != nil || len(pressure) != 1 || pressure[0].TargetID != oldID {
		t.Fatalf("initial pressure = %+v, %v", pressure, err)
	}
	if err := os.Rename(source.memcgDir(path), filepath.Join(source.root, "old")); err != nil {
		t.Fatal(err)
	}
	createMemoryCgroupForTest(t, source.root, path, 95)
	current := oldRef
	current.Key.ID = "current"
	update := applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerCreated, Container: current})
	state := tracker.containers[current.Key.ID]
	id := state.registrationID
	if id == 0 || id == oldID || oldState.registrationID != oldID || len(update.Invalidated) != 0 {
		t.Fatal("directory replacement did not preserve independent registration identities")
	}
	if _, err := tracker.ProcessMemoryEvents(t.Context(), pressure, memoryEventOptions{AcceptPending: true}); err != nil {
		t.Fatal(err)
	}
	if tracker.PendingCount() != 1 {
		t.Fatal("borrowed pressure did not retain its original registration")
	}
	tracker.directory = func(pod.ContainerRef) (os.FileInfo, error) { return oldDirectory, nil }
	late := oldRef
	late.Key.ID = "late-old-container"
	update = applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerCreated, Container: late})
	if len(update.Invalidated) != 0 || tracker.containers[late.Key.ID].registrationID != 0 || state.registrationID != id {
		t.Fatal("late creation retired the current directory watch")
	}
	waitMemoryInvalidationForTest(t, tracker, oldID)
	if oldState.registrationID != 0 || tracker.targets[oldID] != nil || len(tracker.targets) != 1 || tracker.pending[oldID] != nil {
		t.Fatal("native removal retained the old registration or its pending candidate")
	}
	update = applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerDeleted, Container: oldRef})
	if len(update.Invalidated) != 0 || tracker.containers[current.Key.ID] != state || state.registrationID != id {
		t.Fatal("old container deletion affected the replacement")
	}
	if _, err := tracker.ProcessMemoryEvents(t.Context(), []memoryWatchEvent{
		{Kind: memoryThresholdObserved, TargetID: oldID},
		{Kind: memoryTargetRemoved, TargetID: oldID},
	}, memoryEventOptions{AcceptPending: true}); err != nil {
		t.Fatal(err)
	}
	batch := tracker.DrainPending()
	if len(batch) != 1 || batch[0].Container != current || batch[0].RegistrationID != id {
		t.Fatalf("replacement observations = %+v", batch)
	}
}

func TestCgroupTrackerFullViewReleasesDepartedCapacity(t *testing.T) {
	source := newTestCgroupSource(t)
	watcher, err := newMemoryThresholdWatcher(t.Context(), memoryWatchOptions{ThresholdPercent: 90, MaxCgroups: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = watcher.Close() })
	tracker := fixtureCgroupTracker(source, watcher)
	t.Cleanup(tracker.Close)
	for _, path := range []string{"/first", "/second"} {
		createMemoryCgroupForTest(t, source.root, path, 1)
	}
	first, second := containerRefForTest("/first"), containerRefForTest("/second")
	applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerCreated, Container: first})
	_, err = tracker.ProcessContainerEvents(t.Context(), pod.ContainerUpdate{Mode: pod.ContainerUpdateFull, Events: []pod.ContainerEvent{{Container: second}}})
	if err != nil || len(tracker.targets) != 1 || tracker.containers[second.Key.ID].registrationID == 0 {
		t.Fatalf("departed group consumed replacement capacity: %v", err)
	}
}

func TestCgroupTrackerDrainOwnsContainerObservation(t *testing.T) {
	tracker, source := newTestCgroupTracker(t)
	path := "/container"
	createMemoryCgroupForTest(t, source.root, path, 1)
	ref := containerRefForTest(path)
	applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerCreated, Container: ref})
	id := tracker.containers[ref.Key.ID].registrationID
	pressure := []memoryWatchEvent{{Kind: memoryThresholdObserved, TargetID: id}}
	if _, err := tracker.ProcessMemoryEvents(t.Context(), pressure, memoryEventOptions{AcceptPending: true}); err != nil {
		t.Fatal(err)
	}
	batch := tracker.DrainPending()
	if len(batch) != 1 || batch[0].Container != ref {
		t.Fatal("pressure lost the container binding")
	}
	batch[0].Container.Key.ID = "caller-owned"
	if _, err := tracker.ProcessMemoryEvents(t.Context(), pressure, memoryEventOptions{AcceptPending: true}); err != nil {
		t.Fatal(err)
	}
	next := tracker.DrainPending()
	if len(next) != 1 || next[0].Container != ref {
		t.Fatal("caller mutated the tracker binding")
	}
	replacement := ref
	replacement.Key.Generation++
	applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerCreated, Container: replacement})
	tracker.Close()
	if next[0].Container != ref {
		t.Fatal("tracker mutated an owned observation")
	}
}

func TestCgroupTrackerReplacementReleasesPreviousPathCapacity(t *testing.T) {
	source := newTestCgroupSource(t)
	watcher, err := newMemoryThresholdWatcher(t.Context(), memoryWatchOptions{ThresholdPercent: 90, MaxCgroups: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = watcher.Close() })
	tracker := fixtureCgroupTracker(source, watcher)
	t.Cleanup(tracker.Close)
	for _, path := range []string{"/first", "/second"} {
		createMemoryCgroupForTest(t, source.root, path, 1)
	}
	first := containerRefForTest("/first")
	applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerCreated, Container: first})
	oldID := tracker.containers[first.Key.ID].registrationID
	replacement := first
	replacement.Key.Generation++
	replacement.MemoryCgroupPath = "/second"
	update := applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerCreated, Container: replacement})
	state := tracker.containers[replacement.Key.ID]
	if state == nil || state.registrationID == 0 || state.registrationID == oldID || !slices.Contains(update.Invalidated, oldID) ||
		len(tracker.targets) != 1 || state.cgroup.Path != replacement.MemoryCgroupPath {
		t.Fatal("previous path consumed the replacement's registration capacity")
	}
}

func TestCgroupTrackerRegistrationConflictIsTerminal(t *testing.T) {
	for _, mode := range []pod.ContainerUpdateMode{pod.ContainerUpdateFull, pod.ContainerUpdateIncremental} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			tracker, source := newTestCgroupTracker(t)
			path := "/container"
			createMemoryCgroupForTest(t, source.root, path, 95)
			first := containerRefForTest(path)
			applyContainerEventsForTest(t, tracker, pod.ContainerEvent{Kind: pod.ContainerCreated, Container: first})
			owner := tracker.containers[first.Key.ID]
			id := owner.registrationID
			second := first
			second.Key.ID = "second"
			_, err := tracker.ProcessContainerEvents(t.Context(), pod.ContainerUpdate{
				Mode: mode,
				Events: []pod.ContainerEvent{
					{Kind: pod.ContainerCreated, Container: first},
					{Kind: pod.ContainerCreated, Container: second},
				},
			})
			if !errors.Is(err, errCgroupRegistrationConflict) {
				t.Fatalf("conflicting registration error = %v", err)
			}
			if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), second.Key.ID) {
				t.Fatalf("conflict error lacks binding context: %v", err)
			}
			if len(tracker.targets) != 1 || tracker.targets[id] != owner || owner.container != first ||
				tracker.containers[second.Key.ID].registrationID != 0 {
				t.Fatal("conflicting registration overwrote the existing owner")
			}
		})
	}
}
