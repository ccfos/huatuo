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
	controller.resolve = func(_ context.Context, id string, _ *corev1.Container, _ *corev1.ContainerStatus, _ *corev1.Pod) (*containerRecord, error) {
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
	c.resolve = func(_ context.Context, id string, _ *corev1.Container, _ *corev1.ContainerStatus, _ *corev1.Pod) (*containerRecord, error) {
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
	c.resolve = func(context.Context, string, *corev1.Container, *corev1.ContainerStatus, *corev1.Pod) (*containerRecord, error) {
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
	c.resolve = func(context.Context, string, *corev1.Container, *corev1.ContainerStatus, *corev1.Pod) (*containerRecord, error) {
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
			c.resolve = func(context.Context, string, *corev1.Container, *corev1.ContainerStatus, *corev1.Pod) (*containerRecord, error) {
				return containerRecordForTest(id), nil
			}
			snapshot, err, _ := c.refresh(t.Context(), nil, true)
			store.commit(snapshot, err)
			sub := subscribeForTest(t, store)
			if _, err := sub.DrainEvents(); err != nil {
				t.Fatal(err)
			}
			if name == "runtime already removed" {
				c.resolve = func(context.Context, string, *corev1.Container, *corev1.ContainerStatus, *corev1.Pod) (*containerRecord, error) {
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
	c.resolve = func(context.Context, string, *corev1.Container, *corev1.ContainerStatus, *corev1.Pod) (*containerRecord, error) {
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
	c.resolve = func(_ context.Context, id string, _ *corev1.Container, _ *corev1.ContainerStatus, _ *corev1.Pod) (*containerRecord, error) {
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
	c.resolve = func(context.Context, string, *corev1.Container, *corev1.ContainerStatus, *corev1.Pod) (*containerRecord, error) {
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

func TestControllerRefreshesKataRuntimeBinding(t *testing.T) {
	oldProvider := currContainerProvider
	currContainerProvider = containerProviderContainerd
	t.Cleanup(func() { currContainerProvider = oldProvider })
	id := strings.Repeat("a", 64)
	list := runningPodListForTest(id)
	store := newTestContainerStore()
	controller := newContainerController(store)
	controller.fetch = func(context.Context) (corev1.PodList, error) { return list, nil }
	resolves := 0
	controller.resolve = func(_ context.Context,
		id string,
		_ *corev1.Container,
		status *corev1.ContainerStatus,
		_ *corev1.Pod,
	) (*containerRecord, error) {
		resolves++
		record := containerRecordForTest(id)
		record.container.RuntimeType = "io.containerd.kata-qemu.v2"
		record.container.SandboxID = "vm"
		record.container.StartedAt = status.State.Running.StartedAt.Time
		record.container.SyncedAt = time.Unix(int64(resolves), 0)
		return record, nil
	}
	for range 2 {
		snapshot, err, _ := controller.refresh(t.Context(), nil, false)
		if err != nil {
			t.Fatal(err)
		}
		store.commit(snapshot, nil)
	}
	if resolves != 2 {
		t.Errorf("Kata resolutions=%d, want 2", resolves)
	}
	if !store.records[id].container.SyncedAt.Equal(time.Unix(1, 0)) {
		t.Error("refresh changed representative priority")
	}
}

// TestContainerControllerKataRefreshRecovery 验证已发现 Kata 容器的周期恢复：
// 首次发布成功，随后五次运行时查询失败，运行时恢复后应重新查询并清除不可用状态。
func TestContainerControllerKataRefreshRecovery(t *testing.T) {
	useContainerdForTest(t)
	id := strings.Repeat("cafe", 16)
	list := runningPodListForTest(id)
	store := newTestContainerStore()
	controller := newContainerController(store)
	controller.fetch = func(context.Context) (corev1.PodList, error) { return list, nil }
	var isUnavailable bool
	var calls int
	controller.resolve = func(_ context.Context,
		id string, _ *corev1.Container, status *corev1.ContainerStatus, _ *corev1.Pod,
	) (*containerRecord, error) {
		calls++
		if isUnavailable {
			return nil, errors.New("runtime temporarily unavailable")
		}
		record := containerRecordForTest(id)
		record.container.RuntimeType = "io.containerd.kata-qemu.v2"
		record.container.SandboxID = "checkout-sandbox"
		record.container.StartedAt = status.State.Running.StartedAt.Time
		return record, nil
	}
	snapshot, err, _ := controller.refresh(t.Context(), make(map[string]bool), true)
	if err != nil {
		t.Fatalf("initial discovery: %v", err)
	}
	store.commit(snapshot, nil)
	previous := store.records[id]
	isUnavailable = true
	for range 5 {
		var retry bool
		snapshot, err, retry = controller.refresh(t.Context(), make(map[string]bool), false)
		if !retry {
			t.Fatal("known Kata instance stopped retrying")
		}
		if err == nil {
			t.Fatal("runtime outage was not observed")
		}
		store.commit(snapshot, err)
		if store.records[id] != previous || !store.records[id].pod.SyncedAt.Equal(previous.pod.SyncedAt) {
			t.Error("failed runtime query advanced successful sync or replaced cached metadata")
		}
	}
	isUnavailable = false
	beforeRecovery := calls
	snapshot, err, _ = controller.refresh(t.Context(), make(map[string]bool), false)
	store.commit(snapshot, err)
	if err != nil || calls == beforeRecovery || store.err != nil {
		t.Fatalf("healthy runtime never retried: calls before=%d after=%d refresh=%v view=%v",
			beforeRecovery, calls, err, store.err)
	}
}

// TestWorkloadManagerSerialRefresh 验证发现阻塞期间的提示合并到下一轮，
// 同步调用不重叠，停止时取消 I/O 且不提交取消之后返回的发现结果。
func TestWorkloadManagerSerialRefresh(t *testing.T) {
	store := newContainerStore()
	manager := newWorkloadManager(store)
	manager.resyncInterval = time.Hour
	type attempt struct {
		hints map[string]bool
		full  bool
	}
	entered := make(chan attempt, 2)
	release := make(chan struct{})
	var calls int
	manager.discover = func(ctx context.Context, hints map[string]bool, full bool) (containerSnapshot, error, bool) {
		calls++
		entered <- attempt{hints: hints, full: full}
		if calls == 1 {
			select {
			case <-release:
			case <-ctx.Done():
			}
		} else {
			<-ctx.Done()
		}
		record := &containerRecord{pod: &Pod{ID: "checkout-sandbox", Type: TypeCubeSandbox}}
		return containerSnapshot{records: map[string]*containerRecord{record.pod.ID: record}, isComplete: true}, nil, false
	}
	manager.start()
	t.Cleanup(func() { manager.cancel(); <-manager.done })
	first := attempt{}
	select {
	case first = <-entered:
	case <-time.After(time.Second):
		t.Error("initial discovery did not start")
		return
	}
	if !first.full || len(first.hints) != 0 {
		t.Errorf("initial discovery=%+v", first)
	}
	manager.request("checkout-sandbox", false)
	manager.request("checkout-worker", true)
	manager.request("checkout-sandbox", true)
	close(release)
	select {
	case next := <-entered:
		if next.full || len(next.hints) != 2 || !next.hints["checkout-sandbox"] || !next.hints["checkout-worker"] {
			t.Errorf("queued discovery hints=%+v", next)
		}
	case <-time.After(time.Second):
		t.Error("queued refresh did not run after the first discovery")
		return
	}
	store.mu.RLock()
	revision := store.revision
	store.mu.RUnlock()
	manager.cancel()
	select {
	case <-manager.done:
	case <-time.After(time.Second):
		t.Error("manager did not join canceled discovery")
		return
	}
	if calls != 2 || store.revision != revision || len(store.records) != 0 || store.isActive {
		t.Errorf("canceled discovery changed state: calls=%d, revision=%d/%d, records=%d, active=%t",
			calls, store.revision, revision, len(store.records), store.isActive)
	}
}

// TestContainerControllerDuplicateMembers 验证同一列表内和跨 Pod 的重复 ID
// 都保留为 nil 成员，不用冲突元数据覆盖旧视图，也不据此删除其他成员。
func TestContainerControllerDuplicateMembers(t *testing.T) {
	useContainerdForTest(t)
	duplicate, retained, fresh := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	for _, separatePods := range []bool{false, true} {
		t.Run(fmt.Sprintf("separate_pods=%t", separatePods), func(t *testing.T) {
			store := newTestContainerStore()
			previous := containerRecordForTest(duplicate)
			store.commit(containerSnapshot{records: map[string]*containerRecord{
				duplicate: previous, retained: containerRecordForTest(retained),
			}, isComplete: true}, nil)
			list := runningPodListForTest(duplicate, duplicate, fresh)
			if separatePods {
				list = runningPodListForTest(duplicate)
				other := runningPodListForTest(duplicate, fresh)
				list.Items = append(list.Items, other.Items...)
			}
			controller := newContainerController(store)
			controller.fetch = func(context.Context) (corev1.PodList, error) { return list, nil }
			controller.resolve = func(_ context.Context, id string, _ *corev1.Container, _ *corev1.ContainerStatus, _ *corev1.Pod) (*containerRecord, error) {
				record := containerRecordForTest(id)
				record.container.Name = "conflicting-metadata"
				return record, nil
			}
			snapshot, err, retry := controller.refresh(t.Context(), nil, true)
			member, exists := snapshot.records[duplicate]
			if err == nil || !strings.Contains(err.Error(), "duplicate") || snapshot.isComplete || !retry || !exists || member != nil {
				t.Errorf("duplicate result: complete=%t retry=%t member=%+v exists=%t err=%v", snapshot.isComplete, retry, member, exists, err)
			}
			store.commit(snapshot, err)
			if store.records[duplicate] != previous || store.records[retained] == nil || store.records[fresh] == nil {
				t.Error("duplicate list replaced old metadata, deleted an absent member, or lost a valid new member")
			}
		})
	}
}

// TestContainerControllerPendingMetadataKeepsSyncTime 验证有限重试耗尽后仍保留
// 上次成功视图；成员列表完整时，其他已消失对象不受详情失败影响。
func TestContainerControllerPendingMetadataKeepsSyncTime(t *testing.T) {
	useContainerdForTest(t)
	id, removed := strings.Repeat("a", 64), strings.Repeat("b", 64)
	store := newTestContainerStore()
	previous := containerRecordForTest(id)
	store.commit(containerSnapshot{records: map[string]*containerRecord{id: previous, removed: containerRecordForTest(removed)}, isComplete: true}, nil)
	controller := newContainerController(store)
	controller.fetch = func(context.Context) (corev1.PodList, error) { return runningPodListForTest(id), nil }
	controller.resolve = func(context.Context, string, *corev1.Container, *corev1.ContainerStatus, *corev1.Pod) (*containerRecord, error) {
		return nil, errors.New("runtime temporarily unavailable")
	}
	for attempt := range 4 {
		snapshot, err, _ := controller.refresh(t.Context(), nil, attempt == 0)
		member, exists := snapshot.records[id]
		if err == nil || !snapshot.isComplete || !exists || member != nil {
			t.Errorf("pending metadata result: complete=%t exists=%t member=%+v err=%v", snapshot.isComplete, exists, member, err)
		}
		store.commit(snapshot, err)
		if store.records[id] != previous || store.records[removed] != nil {
			t.Error("metadata outage advanced successful sync or blocked confirmed deletion")
		}
	}
}

// TestWorkloadManagerDiscoveryDeadline 验证超时后返回的完整空列表不能清空缓存，
// 下一轮成功后恢复同步；每轮均从公共调度获得独立的截止时间。
func TestWorkloadManagerDiscoveryDeadline(t *testing.T) {
	store := newContainerStore()
	manager := newWorkloadManager(store)
	manager.resyncInterval = time.Hour
	manager.discoveryTimeout = 20 * time.Millisecond
	calls := 0
	manager.discover = func(ctx context.Context, _ map[string]bool, full bool) (containerSnapshot, error, bool) {
		calls++
		if _, ok := ctx.Deadline(); !ok {
			t.Error("discovery has no deadline")
		}
		if calls > 2 && !full {
			t.Error("retry after timeout did not restore full discovery")
		}
		if calls == 2 {
			<-ctx.Done()
			return containerSnapshot{isComplete: true}, nil, false
		}
		record := containerRecordForTest("checkout-api")
		return containerSnapshot{records: map[string]*containerRecord{record.container.ID: record}, isComplete: true}, nil, false
	}
	manager.start()
	t.Cleanup(func() { manager.cancel(); <-manager.done })
	subscriber := subscribeForTest(t, store)
	waitContainerViewForTest(t, subscriber, 1)
	store.mu.RLock()
	previous := store.records["checkout-api"]
	store.mu.RUnlock()
	manager.request("", false)
	deadline := time.After(time.Second)
	for {
		select {
		case <-subscriber.Notify():
		case <-deadline:
			t.Error("discovery deadline was not reported")
			return
		}
		_, err := subscriber.DrainEvents()
		if errors.Is(err, context.DeadlineExceeded) {
			break
		}
	}
	store.mu.RLock()
	retained := store.records["checkout-api"] == previous
	store.mu.RUnlock()
	if !retained {
		t.Error("late empty result changed the cached workload")
	}
	manager.request("checkout-api", false)
	waitContainerViewForTest(t, subscriber, 1)
}

// TestWorkloadManagerInitializationDeadline 验证初始化使用同一超时约束，
// 即使初始化在超时后返回 nil，也不能继续执行发现或发布完整空列表。
func TestWorkloadManagerInitializationDeadline(t *testing.T) {
	store := newContainerStore()
	manager := newWorkloadManager(store)
	manager.discoveryTimeout = 20 * time.Millisecond
	manager.initialize = func(ctx context.Context) error { <-ctx.Done(); return nil }
	manager.discover = func(context.Context, map[string]bool, bool) (containerSnapshot, error, bool) {
		t.Error("discovery ran after initialization timed out")
		return containerSnapshot{isComplete: true}, nil, false
	}
	manager.start()
	t.Cleanup(func() { manager.cancel(); <-manager.done })
	subscriber := subscribeForTest(t, store)
	deadline := time.After(time.Second)
	for {
		select {
		case <-subscriber.Notify():
		case <-deadline:
			t.Error("initialization deadline was not reported")
			return
		}
		_, err := subscriber.DrainEvents()
		if errors.Is(err, context.DeadlineExceeded) {
			return
		}
	}
}

// sourceFixture provides one discovery attempt without a second producer loop.
type sourceFixture struct {
	kind     string
	discover func(context.Context) (Discovery, error)
}

func (s *sourceFixture) Type() string                                    { return s.kind }
func (s *sourceFixture) Discover(ctx context.Context) (Discovery, error) { return s.discover(ctx) }

// TestSourceDiscoveryCompleteness 验证公共来源的成员完整性与错误独立处理，
// 身份冲突阻止删除；nil 成员保留上次视图，来源不能注入采集状态或 CSS。
func TestSourceDiscoveryCompleteness(t *testing.T) {
	raw := func(id string) *Pod { return &Pod{ID: id, Type: TypeCubeSandbox} }
	offline := errors.New("cubelet unavailable")
	for _, test := range []struct {
		name      string
		discovery Discovery
		sourceErr error
		want      []string
		wantErr   bool
		complete  bool
	}{
		{name: "complete empty", discovery: Discovery{Complete: true}, complete: true},
		{name: "nil retains membership", discovery: Discovery{Pods: map[string]*Pod{"checkout-api": nil}, Complete: true}, want: []string{"checkout-api"}, complete: true},
		{name: "complete with conversion error", discovery: Discovery{Pods: map[string]*Pod{"checkout-api": nil}, Complete: true}, sourceErr: offline, want: []string{"checkout-api"}, wantErr: true, complete: true},
		{name: "incomplete adds valid records", discovery: Discovery{Pods: map[string]*Pod{"checkout-worker": raw("checkout-worker")}}, want: []string{"checkout-api", "checkout-old", "checkout-worker"}},
		{name: "query failure", sourceErr: offline, want: []string{"checkout-api", "checkout-old"}, wantErr: true},
		{name: "wrong id", discovery: Discovery{Pods: map[string]*Pod{"checkout-api": raw("conflicting-id")}, Complete: true}, want: []string{"checkout-api", "checkout-old"}, wantErr: true},
		{name: "wrong type", discovery: Discovery{Pods: map[string]*Pod{"checkout-api": {ID: "checkout-api", Type: TypeContainer}}, Complete: true}, want: []string{"checkout-api", "checkout-old"}, wantErr: true},
		{name: "empty member id", discovery: Discovery{Pods: map[string]*Pod{"": nil}, Complete: true}, want: []string{"checkout-api", "checkout-old"}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newTestContainerStore()
			previous := &containerRecord{pod: raw("checkout-api")}
			store.commit(containerSnapshot{records: map[string]*containerRecord{"checkout-api": previous, "checkout-old": {pod: raw("checkout-old")}}, isComplete: true}, nil)
			source := &sourceFixture{kind: TypeCubeSandbox, discover: func(context.Context) (Discovery, error) { return test.discovery, test.sourceErr }}
			manager, err := newSourceManager(store, source, ManagerOptions{})
			if err != nil {
				t.Error(err)
				return
			}
			snapshot, err, retry := manager.discover(t.Context(), nil, true)
			if (err != nil) != test.wantErr || retry != test.wantErr || snapshot.isComplete != test.complete {
				t.Errorf("discovery complete=%t retry=%t err=%v", snapshot.isComplete, retry, err)
			}
			store.commit(snapshot, err)
			if len(store.records) != len(test.want) {
				t.Errorf("members=%d, want %d", len(store.records), len(test.want))
			}
			for _, id := range test.want {
				if store.records[id] == nil {
					t.Errorf("missing retained member %q", id)
				}
			}
			if current := store.records["checkout-api"]; current != nil && current != previous {
				t.Error("unresolved member replaced the last successful view")
			}
		})
	}
}

// TestSourceManagerValidation 验证只接受明确来源和有效周期，重复启动不能
// 覆盖已选来源；检查发生在探针初始化或后台查询之前。
func TestSourceManagerValidation(t *testing.T) {
	source := &sourceFixture{kind: TypeCubeSandbox, discover: func(context.Context) (Discovery, error) { return Discovery{}, nil }}
	for _, test := range []struct {
		name    string
		source  Source
		options ManagerOptions
	}{
		{name: "missing source"},
		{name: "unknown source", source: &sourceFixture{kind: "unknown"}},
		{name: "negative interval", source: source, options: ManagerOptions{SyncInterval: -time.Second}},
		{name: "negative timeout", source: source, options: ManagerOptions{DiscoveryTimeout: -time.Second}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := newSourceManager(newContainerStore(), test.source, test.options); err == nil {
				t.Error("invalid source configuration accepted")
			}
		})
	}
	manager, err := newSourceManager(newContainerStore(), source, ManagerOptions{SyncInterval: time.Minute, DiscoveryTimeout: time.Second})
	if err != nil {
		t.Error(err)
		return
	}
	if manager.resyncInterval != time.Minute || manager.discoveryTimeout != time.Second {
		t.Error("source scheduling options were not applied")
	}
	workloadManagerMu.Lock()
	previous := activeManager
	activeManager = manager
	workloadManagerMu.Unlock()
	t.Cleanup(func() { workloadManagerMu.Lock(); activeManager = previous; workloadManagerMu.Unlock() })
	if err := StartManager(source, ManagerOptions{}); err == nil {
		t.Error("second source replaced the active manager")
	}
}

// TestSourceManagerCancellation 验证 Source.Discover 接收公共调度的截止时间，
// 停止会等待当前调用结束；取消后返回的结果不进入 Store。
func TestSourceManagerCancellation(t *testing.T) {
	entered := make(chan struct{})
	source := &sourceFixture{kind: TypeCubeSandbox, discover: func(ctx context.Context) (Discovery, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("source received no discovery deadline")
		}
		close(entered)
		<-ctx.Done()
		return Discovery{Complete: true, Pods: map[string]*Pod{"late": {ID: "late", Type: TypeCubeSandbox}}}, nil
	}}
	store := newContainerStore()
	manager, err := newSourceManager(store, source, ManagerOptions{})
	if err != nil {
		t.Error(err)
		return
	}
	manager.start()
	t.Cleanup(func() { manager.cancel(); <-manager.done })
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Error("source did not start")
		return
	}
	manager.cancel()
	select {
	case <-manager.done:
	case <-time.After(time.Second):
		t.Error("source cancellation did not complete")
		return
	}
	if store.revision != 0 || len(store.records) != 0 {
		t.Error("late source result was published")
	}
}
