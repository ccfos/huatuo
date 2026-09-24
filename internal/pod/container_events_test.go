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
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func newTestContainerStore() *containerStore {
	store := newContainerStore()
	store.isActive, store.err = true, nil
	return store
}

func containerRecordForTest(id string) *containerRecord {
	return &containerRecord{
		container: &Container{ID: id, InitPid: 42, Type: ContainerTypeNormal},
		ref:       ContainerRef{Key: ContainerKey{ID: id}, InitPID: 42, MemoryCgroupPath: "/" + id}, startTime: 100,
	}
}

func subscribeForTest(t testing.TB, store *containerStore) *ContainerSubscription {
	t.Helper()
	s, err := store.subscribe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestContainerSubscriptionFullAndIncrementalUpdates(t *testing.T) {
	store := newTestContainerStore()
	first := containerRecordForTest("first")
	store.commit(map[string]*containerRecord{"first": first}, nil)
	sub := subscribeForTest(t, store)
	// This change is covered by the initial full update, not also delivered as a delta.
	second := containerRecordForTest("second")
	records := map[string]*containerRecord{"first": first, "second": second}
	store.commit(records, nil)
	initial, err := sub.DrainEvents()
	if err != nil || initial.Mode != ContainerUpdateFull || len(initial.Events) != 2 {
		t.Fatalf("initial=%+v, %v", initial, err)
	}

	for _, event := range initial.Events {
		record := records[event.Container.Key.ID]
		if event.Kind != ContainerEventUnknown || record == nil || event.Container != record.ref {
			t.Fatalf("full update entry=%+v", event)
		}
	}

	revision := initial.Revision
	borrowed := slices.Clone(initial.Events)
	delete(records, "first")
	store.commit(records, nil)
	if !slices.Equal(initial.Events, borrowed) {
		t.Fatal("producer mutated borrowed view")
	}
	next, err := sub.DrainEvents()
	if err != nil || next.Mode != ContainerUpdateIncremental || len(next.Events) != 1 || next.Events[0].Kind != ContainerDeleted || next.Events[0].Container.Key != first.ref.Key || next.Revision <= revision {
		t.Fatalf("increment=%+v, %v", next, err)
	}

	store.commit(nil, errors.New("kubelet query failed"))
	if _, err := sub.DrainEvents(); !errors.Is(err, ErrContainersUnavailable) {
		t.Fatal(err)
	}

	store.commit(records, nil)
	recovered, err := sub.DrainEvents()
	if err != nil || recovered.Mode != ContainerUpdateFull || !slices.Equal(recovered.Events, []ContainerEvent{{Container: second.ref}}) {
		t.Fatalf("recovered=%+v, %v", recovered, err)
	}
	if idle, err := sub.DrainEvents(); err != nil || idle.Mode != ContainerUpdateIncremental || len(idle.Events) != 0 {
		t.Fatal("idle drain changed state")
	}
}

func TestContainerSubscriptionOverflowRepairsLostDelete(t *testing.T) {
	store := newTestContainerStore()
	first := containerRecordForTest("first")
	store.commit(map[string]*containerRecord{"first": first}, nil)
	slow, fast := subscribeForTest(t, store), subscribeForTest(t, store)
	_, _ = slow.DrainEvents()
	_, _ = fast.DrainEvents()
	records := make(map[string]*containerRecord)
	store.commit(records, nil)
	for i := 0; i < containerEventQueueSize+1; i++ {
		id := fmt.Sprint(i)
		records[id] = containerRecordForTest(id)
		store.commit(records, nil)
		update, err := fast.DrainEvents()
		if err != nil || update.Mode != ContainerUpdateIncremental {
			t.Fatal("slow consumer forced a full update on the fast consumer")
		}
	}
	update, err := slow.DrainEvents()
	if err != nil || update.Mode != ContainerUpdateFull || len(update.Events) != len(records) {
		t.Fatalf("overflow recovery=%+v, %v", update, err)
	}
	for _, event := range update.Events {
		if event.Kind != ContainerEventUnknown {
			t.Fatal("full update retained an incremental event kind")
		}
		if event.Container.Key == first.ref.Key {
			t.Fatal("lost delete survived the full update")
		}
	}
}

func TestContainerSubscriptionUnavailableIsNotEmpty(t *testing.T) {
	store := newTestContainerStore()
	store.err = ErrContainersUnavailable
	sub := subscribeForTest(t, store)
	if update, err := sub.DrainEvents(); !errors.Is(err, ErrContainersUnavailable) || update.Mode != ContainerUpdateUnknown {
		t.Fatal("uninitialized view became an empty full update")
	}
	first := containerRecordForTest("first")
	store.commit(map[string]*containerRecord{"first": first}, nil)
	_, _ = sub.DrainEvents()
	store.commit(nil, errors.New("kubelet query failed"))
	if update, err := sub.DrainEvents(); !errors.Is(err, ErrContainersUnavailable) || update.Mode != ContainerUpdateUnknown || len(store.records) != 1 {
		t.Fatal("failed sync deleted containers")
	}
	store.commit(nil, nil)
	update, err := sub.DrainEvents()
	if err != nil || update.Mode != ContainerUpdateFull || len(update.Events) != 0 {
		t.Fatal("complete empty view did not produce a full update")
	}
}

func TestContainerSubscriptionInstanceGeneration(t *testing.T) {
	store := newTestContainerStore()
	first := containerRecordForTest("first")
	store.commit(map[string]*containerRecord{"first": first}, nil)
	sub := subscribeForTest(t, store)
	_, _ = sub.DrainEvents()
	metadata := containerRecordForTest("first")
	metadata.container.Name = "new label"
	store.commit(map[string]*containerRecord{"first": metadata}, nil)
	if metadata.ref.Key != first.ref.Key {
		t.Fatal("metadata update changed generation")
	}
	if update, _ := sub.DrainEvents(); len(update.Events) != 0 {
		t.Fatal("metadata update emitted lifecycle")
	}
	replacement := containerRecordForTest("first")
	replacement.startTime++
	store.commit(map[string]*containerRecord{"first": replacement}, nil)
	update, err := sub.DrainEvents()
	if err != nil || len(update.Events) != 2 || update.Events[0].Kind != ContainerDeleted || update.Events[1].Kind != ContainerCreated || replacement.ref.Key.Generation <= first.ref.Key.Generation {
		t.Fatalf("replacement=%+v, %v", update, err)
	}
}

func TestContainerSubscriptionBatchAndClose(t *testing.T) {
	store := newTestContainerStore()
	records := make(map[string]*containerRecord)
	for i := 0; i < containerEventBatchSize+1; i++ {
		id := fmt.Sprint(i)
		records[id] = containerRecordForTest(id)
	}
	store.commit(records, nil)
	sub := subscribeForTest(t, store)
	full, err := sub.DrainEvents()
	if err != nil || full.Mode != ContainerUpdateFull || len(full.Events) != len(records) || cap(full.Events) != len(full.Events) {
		t.Fatalf("full update=%+v, %v", full, err)
	}

	// A large full update must not raise the incremental batch limit.
	store.commit(nil, nil)
	update, err := sub.DrainEvents()
	if err != nil || update.Mode != ContainerUpdateIncremental || len(update.Events) != containerEventBatchSize || cap(update.Events) != len(update.Events) {
		t.Fatal("unbounded batch")
	}
	select {
	case <-sub.Notify():
	default:
		t.Fatal("remaining batch lost wakeup")
	}
	tail, err := sub.DrainEvents()
	if err != nil || len(tail.Events) != 1 || tail.Events[0].Kind != ContainerDeleted {
		t.Fatal("lost batch tail")
	}
	borrowed := slices.Clone(tail.Events)
	sub.Close()
	sub.Close()
	if !slices.Equal(tail.Events, borrowed) {
		t.Fatal("Close mutated borrowed data")
	}
	if _, err := sub.DrainEvents(); !errors.Is(err, ErrContainerSubscriptionClosed) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	other, err := store.subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	timeout := time.After(time.Second)
	for {
		select {
		case _, ok := <-other.Notify():
			if !ok {
				return
			}
		case <-timeout:
			t.Fatal("context cancellation did not close subscription")
		}
	}
}

func TestContainerQueriesPreserveHostCollectorCompatibility(t *testing.T) {
	previous := containerView
	containerView = newTestContainerStore()
	t.Cleanup(func() { containerView = previous })
	record := containerRecordForTest("cached")
	containerView.commit(map[string]*containerRecord{"cached": record}, nil)
	containerView.commit(nil, errors.New("kubelet temporarily unavailable"))
	cached, err := NormalContainers()
	if err != nil || cached["cached"] != record.container {
		t.Fatal("query lost last committed metadata")
	}
	single, err := ContainerByID("cached")
	if err != nil || single != record.container {
		t.Fatal("direct lookup lost cached metadata")
	}
	if _, err := ContainerRefByID("cached"); !errors.Is(err, ErrContainersUnavailable) {
		t.Fatal("strict lookup hid synchronization failure")
	}
	containerView = newContainerStore()
	empty, err := Containers()
	if err != nil || len(empty) != 0 {
		t.Fatal("disabled pod manager blocked host collectors")
	}
	if _, err := SubscribeContainers(t.Context()); !errors.Is(err, ErrContainerManagerDisabled) {
		t.Fatal("subscription did not report disabled producer")
	}
}

func TestMemoryCgroupDirectoryPreservesPublishedIdentity(t *testing.T) {
	store := newTestContainerStore()
	record := containerRecordForTest("container")
	directory := t.TempDir()
	path := filepath.Join(directory, "current")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	published, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	record.directory = published
	store.commit(map[string]*containerRecord{record.ref.Key.ID: record}, nil)
	if err := os.Rename(path, filepath.Join(directory, "old")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	actual, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.memoryCgroupDirectory(record.ref)
	if err != nil || !os.SameFile(got, published) || os.SameFile(got, actual) {
		t.Fatalf("published identity was replaced by current path: %v", err)
	}
	stale := record.ref
	stale.Key.Generation--
	if _, err := store.memoryCgroupDirectory(stale); err == nil {
		t.Fatal("stale container reference returned directory identity")
	}
	store.err = ErrContainersUnavailable
	if _, err := store.memoryCgroupDirectory(record.ref); !errors.Is(err, ErrContainersUnavailable) {
		t.Fatalf("unavailable directory lookup = %v", err)
	}
}
