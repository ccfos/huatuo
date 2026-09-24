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
	records, err, retry := c.refresh(t.Context(), nil, true)
	if err != nil || retry || len(records) != 2 || records[first] == nil || records[sidecar] == nil {
		t.Fatalf("scope=%v, err=%v", records, err)
	}
	store.commit(records, err)
	records, err, _ = c.refresh(t.Context(), map[string]bool{sidecar: false}, false)
	if err != nil || counts[first] != 1 || counts[sidecar] != 2 {
		t.Fatal("increment resolved unrelated containers")
	}
	store.commit(records, err)
	c.fetch = func(context.Context) (corev1.PodList, error) { return corev1.PodList{}, errors.New("query failed") }
	records, err, retry = c.refresh(t.Context(), nil, false)
	store.commit(records, err)
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
		records, err, retry := c.refresh(t.Context(), nil, i == 0)
		store.commit(records, err)
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
	records, err, retry := c.refresh(t.Context(), map[string]bool{id: false}, false)
	if err != nil || retry || records[id] == nil {
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
