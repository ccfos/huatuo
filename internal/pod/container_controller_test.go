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
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

func useContainerdForTest(t *testing.T) {
	t.Helper()
	initMu.Lock()
	previous := currContainerProvider
	currContainerProvider = containerProviderContainerd
	initMu.Unlock()
	t.Cleanup(func() { initMu.Lock(); currContainerProvider = previous; initMu.Unlock() })
}

func runningPodListForTest(ids ...string) corev1.PodList {
	p := corev1.Pod{}
	for _, id := range ids {
		p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: id})
		p.Status.ContainerStatuses = append(p.Status.ContainerStatuses, corev1.ContainerStatus{
			Name: id, ContainerID: "containerd://" + id,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		})
	}
	return corev1.PodList{Items: []corev1.Pod{p}}
}

func TestSynchronizedContainersQueryBoundaries(t *testing.T) {
	useContainerdForTest(t)
	previous := containerView
	containerView = newContainerStore()
	t.Cleanup(func() { containerView = previous })
	if got, err := SynchronizedContainers(); err != nil || len(got) != 0 {
		t.Fatalf("disabled view = %v, %v", got, err)
	}

	store := newTestContainerStore()
	containerView = store
	controller := newContainerController(store)
	id := strings.Repeat("a", 64)
	list := runningPodListForTest(id)
	var queryErr error
	queries := 0
	controller.fetch = func(context.Context) (corev1.PodList, error) {
		queries++
		return list, queryErr
	}
	controller.resolve = func(id string, _ *corev1.Container, _ *corev1.ContainerStatus, _ *corev1.Pod) (*containerRecord, error) {
		record := containerRecordForTest(id)
		record.container.Qos = ContainerQosLevelMin
		record.container.CgroupCss = map[string]uint64{"blkio": 123}
		return record, nil
	}
	refresh := func() {
		snapshot, err, _ := controller.refresh(t.Context(), nil, true)
		store.commit(snapshot, err)
	}
	refresh()
	got, err := SynchronizedContainers()
	if err != nil || len(got) != 1 || got[id].CgroupCss["blkio"] != 123 {
		t.Fatalf("available view = %v, %v", got, err)
	}
	delete(got, id)
	if next, err := SynchronizedContainers(); err != nil || len(next) != 1 || queries != 1 {
		t.Fatalf("copied view / shared producer = %v, %v, queries=%d", next, err, queries)
	}

	queryErr = errors.New("kubelet query failed")
	refresh()
	if got, err := SynchronizedContainers(); got != nil || !errors.Is(err, queryErr) {
		t.Fatalf("failed strict view = %v, %v", got, err)
	}
	if cached, err := Containers(); err != nil || len(cached) != 1 {
		t.Fatalf("ordinary cached view changed = %v, %v", cached, err)
	}

	queryErr = nil
	list = corev1.PodList{}
	refresh()
	if got, err := SynchronizedContainers(); err != nil || len(got) != 0 {
		t.Fatalf("confirmed empty view = %v, %v", got, err)
	}
	list = runningPodListForTest(id)
	refresh()
	if got, err := SynchronizedContainers(); err != nil || len(got) != 1 {
		t.Fatalf("recovered view = %v, %v", got, err)
	}
}

func TestContainerControllerIndependentOfGetters(t *testing.T) {
	useContainerdForTest(t)
	store := newContainerStore()
	controller := newContainerController(store)
	first, second := strings.Repeat("a", 64), strings.Repeat("b", 64)
	var mu sync.Mutex
	list := runningPodListForTest(first)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	controller.fetch = func(ctx context.Context) (corev1.PodList, error) {
		once.Do(func() {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
		})
		mu.Lock()
		defer mu.Unlock()
		return list, nil
	}
	controller.resolve = func(id string, _ *corev1.Container, _ *corev1.ContainerStatus, _ *corev1.Pod) (*containerRecord, error) {
		return containerRecordForTest(id), nil
	}
	controller.start()
	t.Cleanup(func() { controller.cancel(); <-controller.done })
	sub := subscribeForTest(t, store)
	<-entered
	mu.Lock()
	list = runningPodListForTest(first, second)
	mu.Unlock()
	controller.request(second, false)
	close(release)
	waitContainerViewForTest(t, sub, 2)
	mu.Lock()
	list = runningPodListForTest(second)
	mu.Unlock()
	controller.request(first, true)
	timeout := time.After(time.Second)
	for {
		select {
		case <-sub.Notify():
		case <-timeout:
			t.Fatal("container deletion required a getter")
		}
		update, err := sub.DrainEvents()
		if err != nil {
			t.Fatal(err)
		}
		if update.Mode == ContainerUpdateFull && len(update.Events) == 1 {
			return
		}
		for _, event := range update.Events {
			if event.Kind == ContainerDeleted && event.Container.Key.ID == first {
				return
			}
		}
	}
}

func waitContainerViewForTest(t *testing.T, sub *ContainerSubscription, count int) {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case <-sub.Notify():
		case <-timeout:
			t.Fatal("container view did not become available")
		}
		update, err := sub.DrainEvents()
		if errors.Is(err, ErrContainersUnavailable) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if update.Mode == ContainerUpdateFull && len(update.Events) == count {
			return
		}
	}
}

func TestContainerControllerScopeAndPartialFailure(t *testing.T) {
	useContainerdForTest(t)
	first, second, sidecar, initID := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64)
	list := runningPodListForTest(first, second)
	list.Items[0].Status.ContainerStatuses[1].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{}}
	always := corev1.ContainerRestartPolicyAlways
	list.Items[0].Spec.InitContainers = []corev1.Container{{Name: sidecar, RestartPolicy: &always}, {Name: initID}}
	list.Items[0].Status.InitContainerStatuses = runningPodListForTest(sidecar, initID).Items[0].Status.ContainerStatuses
	store := newTestContainerStore()
	c := newContainerController(store)
	c.fetch = func(context.Context) (corev1.PodList, error) { return list, nil }
	counts := make(map[string]int)
	c.resolve = func(id string, _ *corev1.Container, _ *corev1.ContainerStatus, _ *corev1.Pod) (*containerRecord, error) {
		counts[id]++
		return containerRecordForTest(id), nil
	}
	snapshot, err, retry := c.refresh(t.Context(), nil, true)
	if err != nil || retry || len(snapshot.records) != 2 || snapshot.records[first] == nil || snapshot.records[sidecar] == nil {
		t.Fatalf("scope=%v, err=%v", snapshot.records, err)
	}
	store.commit(snapshot, err)
	snapshot, err, _ = c.refresh(t.Context(), map[string]bool{sidecar: false}, false)
	if err != nil || counts[first] != 1 || counts[sidecar] != 2 {
		t.Fatal("increment resolved unrelated containers")
	}
	store.commit(snapshot, err)
	c.fetch = func(context.Context) (corev1.PodList, error) { return corev1.PodList{}, errors.New("query failed") }
	snapshot, err, retry = c.refresh(t.Context(), nil, false)
	store.commit(snapshot, err)
	if err == nil || !retry || len(store.records) != 2 {
		t.Fatal("failed query deleted cached containers")
	}
}

func TestContainerControllerReadinessRetriesAreBounded(t *testing.T) {
	useContainerdForTest(t)
	id := strings.Repeat("a", 64)
	store := newTestContainerStore()
	c := newContainerController(store)
	c.fetch = func(context.Context) (corev1.PodList, error) { return runningPodListForTest(id), nil }
	attempts := 0
	c.resolve = func(string, *corev1.Container, *corev1.ContainerStatus, *corev1.Pod) (*containerRecord, error) {
		attempts++
		return nil, errors.New("init pid not ready")
	}
	for i := 0; i < 4; i++ {
		snapshot, err, retry := c.refresh(t.Context(), nil, i == 0)
		store.commit(snapshot, err)
		if err == nil || retry != (i < 2) {
			t.Fatalf("attempt %d: err=%v retry=%t", i, err, retry)
		}
	}
	if attempts != 3 {
		t.Fatalf("attempts=%d", attempts)
	}
	c.resolve = func(string, *corev1.Container, *corev1.ContainerStatus, *corev1.Pod) (*containerRecord, error) {
		return containerRecordForTest(id), nil
	}
	snapshot, err, retry := c.refresh(t.Context(), map[string]bool{id: false}, false)
	if err != nil || retry || snapshot.records[id] == nil {
		t.Fatal("new hint did not recover readiness")
	}
}

func TestContainerControllerShutdownCancelsFetch(t *testing.T) {
	store := newContainerStore()
	c := newContainerController(store)
	entered := make(chan struct{})
	c.fetch = func(ctx context.Context) (corev1.PodList, error) {
		close(entered)
		<-ctx.Done()
		return corev1.PodList{}, ctx.Err()
	}
	c.start()
	sub := subscribeForTest(t, store)
	<-entered
	c.cancel()
	select {
	case <-c.done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not join I/O")
	}
	if _, err := sub.DrainEvents(); !errors.Is(err, ErrContainerSubscriptionClosed) {
		t.Fatal(err)
	}
}

func TestContainerControllerDeletionBeforeKubeletUpdate(t *testing.T) {
	for _, name := range []string{"metadata available", "runtime already removed"} {
		t.Run(name, func(t *testing.T) {
			useContainerdForTest(t)
			id := strings.Repeat("a", 64)
			store := newTestContainerStore()
			c := newContainerController(store)
			list := runningPodListForTest(id)
			c.fetch = func(context.Context) (corev1.PodList, error) { return list, nil }
			c.resolve = func(string, *corev1.Container, *corev1.ContainerStatus, *corev1.Pod) (*containerRecord, error) {
				return containerRecordForTest(id), nil
			}
			snapshot, err, _ := c.refresh(t.Context(), nil, true)
			store.commit(snapshot, err)
			sub := subscribeForTest(t, store)
			if _, err := sub.DrainEvents(); err != nil {
				t.Fatal(err)
			}
			if name == "runtime already removed" {
				c.resolve = func(string, *corev1.Container, *corev1.ContainerStatus, *corev1.Pod) (*containerRecord, error) {
					return nil, errors.New("init pid file no longer exists")
				}
			}
			hints := map[string]bool{id: true}
			for range 4 {
				snapshot, err, retry := c.refresh(t.Context(), hints, false)
				store.commit(snapshot, err)
				if !retry || store.records[id] == nil {
					t.Fatalf("unconfirmed deletion: retry=%t, cached=%t", retry, store.records[id] != nil)
				}
				if update, err := sub.DrainEvents(); err != nil || len(update.Events) != 0 {
					t.Fatalf("deletion hint changed the view: update=%+v, err=%v", update, err)
				}
				hints = nil
			}
			list = corev1.PodList{}
			snapshot, err, retry := c.refresh(t.Context(), nil, false)
			store.commit(snapshot, err)
			if err != nil || retry || store.records[id] != nil {
				t.Fatalf("confirmed deletion: cached=%t, retry=%t, err=%v", store.records[id] != nil, retry, err)
			}
			update, err := sub.DrainEvents()
			if err != nil || len(update.Events) != 1 || update.Events[0].Kind != ContainerDeleted || update.Events[0].Container.Key.ID != id {
				t.Fatalf("missing deletion event: update=%+v, err=%v", update, err)
			}
		})
	}
}

func TestContainerControllerUnmatchedCreationHintExpires(t *testing.T) {
	useContainerdForTest(t)
	id, unmatched := strings.Repeat("a", 64), strings.Repeat("b", 64)
	store := newTestContainerStore()
	store.commit(containerSnapshot{records: map[string]*containerRecord{id: containerRecordForTest(id)}, isComplete: true}, nil)
	sub := subscribeForTest(t, store)
	if _, err := sub.DrainEvents(); err != nil {
		t.Fatal(err)
	}
	c := newContainerController(store)
	c.fetch = func(context.Context) (corev1.PodList, error) { return corev1.PodList{}, nil }
	hints := map[string]bool{unmatched: false}
	for attempt := range 4 {
		snapshot, err, retry := c.refresh(t.Context(), hints, false)
		store.commit(snapshot, err)
		if len(store.records) != 0 {
			t.Fatal("unmatched hint retained a container absent from a complete list")
		}
		if retry != (attempt < 2) || (err != nil) != (attempt < 2) {
			t.Fatalf("attempt=%d, retry=%t, err=%v", attempt, retry, err)
		}
		hints = nil
	}
	update, err := sub.DrainEvents()
	if err != nil || update.Mode != ContainerUpdateFull || len(update.Events) != 0 {
		t.Fatalf("expired hint blocked recovery: update=%+v, err=%v", update, err)
	}
}

func TestContainerControllerMetadataFailureDoesNotPreventDeletion(t *testing.T) {
	useContainerdForTest(t)
	deleted, healthy, unresolved := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	store := newTestContainerStore()
	previousView := containerView
	containerView = store
	t.Cleanup(func() { containerView = previousView })
	store.commit(containerSnapshot{records: map[string]*containerRecord{
		deleted: containerRecordForTest(deleted), healthy: containerRecordForTest(healthy),
	}, isComplete: true}, nil)
	sub := subscribeForTest(t, store)
	if _, err := sub.DrainEvents(); err != nil {
		t.Fatal(err)
	}
	c := newContainerController(store)
	c.fetch = func(context.Context) (corev1.PodList, error) { return runningPodListForTest(healthy, unresolved), nil }
	c.resolve = func(string, *corev1.Container, *corev1.ContainerStatus, *corev1.Pod) (*containerRecord, error) {
		return nil, errors.New("init pid not ready")
	}
	snapshot, err, _ := c.refresh(t.Context(), nil, false)
	if err == nil || !snapshot.isComplete {
		t.Fatalf("metadata failure changed membership completeness: complete=%t, err=%v", snapshot.isComplete, err)
	}
	store.commit(snapshot, err)
	if cached, err := ContainerByID(deleted); err != nil || cached != nil {
		t.Fatalf("deleted container still available: cached=%v, err=%v", cached, err)
	}
	if store.records[healthy] == nil || store.records[unresolved] != nil {
		t.Fatal("partial metadata resolution lost a healthy container or published a nil record")
	}
	if _, err := sub.DrainEvents(); !errors.Is(err, ErrContainersUnavailable) {
		t.Fatalf("incomplete metadata view became available: %v", err)
	}
	c.resolve = func(id string, _ *corev1.Container, _ *corev1.ContainerStatus, _ *corev1.Pod) (*containerRecord, error) {
		return containerRecordForTest(id), nil
	}
	snapshot, err, _ = c.refresh(t.Context(), nil, false)
	store.commit(snapshot, err)
	update, err := sub.DrainEvents()
	if err != nil || update.Mode != ContainerUpdateFull || len(update.Events) != 2 {
		t.Fatalf("recovery did not publish a full view: update=%+v, err=%v", update, err)
	}
	for _, event := range update.Events {
		if event.Container.Key.ID == deleted {
			t.Fatal("recovery resurrected the deleted container")
		}
	}
}

func TestContainerControllerIncompleteListPreservesMetadata(t *testing.T) {
	for _, name := range []string{"request failure", "missing spec", "invalid container id"} {
		t.Run(name, func(t *testing.T) {
			useContainerdForTest(t)
			id, other := strings.Repeat("a", 64), strings.Repeat("b", 64)
			store := newTestContainerStore()
			store.commit(containerSnapshot{records: map[string]*containerRecord{id: containerRecordForTest(id)}, isComplete: true}, nil)
			c := newContainerController(store)
			list := runningPodListForTest(other)
			switch name {
			case "missing spec":
				list.Items[0].Spec.Containers = nil
			case "invalid container id":
				list.Items[0].Status.ContainerStatuses[0].ContainerID = "containerd://invalid"
			}
			c.fetch = func(context.Context) (corev1.PodList, error) {
				if name == "request failure" {
					return corev1.PodList{}, errors.New("kubelet unavailable")
				}
				return list, nil
			}
			snapshot, err, _ := c.refresh(t.Context(), nil, false)
			if err == nil || snapshot.isComplete {
				t.Fatalf("invalid list accepted: complete=%t, err=%v", snapshot.isComplete, err)
			}
			store.commit(snapshot, err)
			if store.records[id] == nil {
				t.Fatal("incomplete list deleted cached metadata")
			}
		})
	}
}

func TestContainerControllerReconcilesWithoutHint(t *testing.T) {
	useContainerdForTest(t)
	id := strings.Repeat("a", 64)
	store := newContainerStore()
	c := newContainerController(store)
	c.resyncInterval = 25 * time.Millisecond
	var mu sync.Mutex
	list := runningPodListForTest(id)
	c.fetch = func(context.Context) (corev1.PodList, error) {
		mu.Lock()
		defer mu.Unlock()
		return list, nil
	}
	c.resolve = func(string, *corev1.Container, *corev1.ContainerStatus, *corev1.Pod) (*containerRecord, error) {
		return containerRecordForTest(id), nil
	}
	c.start()
	t.Cleanup(func() { c.cancel(); <-c.done })
	sub := subscribeForTest(t, store)
	waitContainerViewForTest(t, sub, 1)
	mu.Lock()
	list = corev1.PodList{}
	mu.Unlock()
	select {
	case <-sub.Notify():
	case <-time.After(2 * time.Second):
		t.Fatal("missing CSS hint prevented periodic reconciliation")
	}
	update, err := sub.DrainEvents()
	if err != nil || len(update.Events) != 1 || update.Events[0].Kind != ContainerDeleted || update.Events[0].Container.Key.ID != id {
		t.Fatalf("missing deletion event: update=%+v, err=%v", update, err)
	}
}
