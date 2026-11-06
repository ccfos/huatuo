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

	containerdiscovery "github.com/ccfos/huatuo/internal/pod/container"
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

func containerPodForTest(id string) *Pod {
	return &Pod{
		ID: id, Type: TypeContainer, Name: id,
		Cgroups:   CgroupBinding{Unified: "/" + id},
		Container: &ContainerInfo{Type: ContainerTypeNormal},
		Labels:    map[string]any{labelHostNamespace: "default"},
	}
}

func containerSourceForTest(items []corev1.Pod) *containerSource {
	return &containerSource{
		fetch: func(context.Context) (corev1.PodList, error) { return corev1.PodList{Items: items}, nil },
		resolve: func(_ context.Context, id string, _ *corev1.Container, _ *corev1.ContainerStatus, _ *corev1.Pod) (*Pod, error) {
			return containerPodForTest(id), nil
		},
	}
}

func sourceManagerForTest(t testing.TB, store *containerStore, source Source) *workloadManager {
	t.Helper()
	manager, err := newSourceManager(store, source, ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func TestContainerSourceScope(t *testing.T) {
	useContainerdForTest(t)
	first, waiting, sidecar, initID := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64)
	list := runningPodListForTest(first, waiting)
	list.Items[0].Status.ContainerStatuses[1].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{}}
	always := corev1.ContainerRestartPolicyAlways
	list.Items[0].Spec.InitContainers = []corev1.Container{{Name: sidecar, RestartPolicy: &always}, {Name: initID}}
	list.Items[0].Status.InitContainerStatuses = runningPodListForTest(sidecar, initID).Items[0].Status.ContainerStatuses
	list.Items[0].Status.EphemeralContainerStatuses = runningPodListForTest(strings.Repeat("e", 64)).Items[0].Status.ContainerStatuses
	source := containerSourceForTest(list.Items)
	counts := make(map[string]int)
	source.resolve = func(_ context.Context, id string, _ *corev1.Container, _ *corev1.ContainerStatus, _ *corev1.Pod) (*Pod, error) {
		counts[id]++
		return containerPodForTest(id), nil
	}
	for range 2 {
		discovery, err := source.Discover(t.Context())
		if err != nil || !discovery.Complete || len(discovery.Pods) != 2 || discovery.Pods[first] == nil || discovery.Pods[sidecar] == nil {
			t.Errorf("running scope=%+v, err=%v", discovery, err)
		}
	}
	if counts[first] != 2 || counts[sidecar] != 2 {
		t.Errorf("source did not refresh every member: %v", counts)
	}
}

// Confirmed membership deletion must proceed even when another member fails.
func TestContainerSourceMetadataRecovery(t *testing.T) {
	useContainerdForTest(t)
	for _, kata := range []bool{false, true} {
		t.Run(fmt.Sprintf("kata=%t", kata), func(t *testing.T) {
			id, removed := strings.Repeat("a", 64), strings.Repeat("b", 64)
			source := containerSourceForTest(runningPodListForTest(id, removed).Items)
			failed := false
			calls := 0
			source.resolve = func(_ context.Context, id string, _ *corev1.Container, _ *corev1.ContainerStatus, _ *corev1.Pod) (*Pod, error) {
				calls++
				if failed {
					return nil, errors.New("runtime temporarily unavailable")
				}
				p := containerPodForTest(id)
				if kata {
					p.Container.RuntimeType, p.Container.SandboxID = "io.containerd.kata-qemu.v2", "checkout-vm"
				}
				return p, nil
			}
			store := newTestContainerStore()
			manager := sourceManagerForTest(t, store, source)
			snapshot, err, _ := manager.discover(t.Context(), nil, true)
			if err != nil {
				t.Fatal(err)
			}
			store.commit(snapshot, nil)
			previous := store.records[id]
			previous.pod.lifeResources["baseline"] = 6400
			failed = true
			source.fetch = func(context.Context) (corev1.PodList, error) { return runningPodListForTest(id), nil }
			for range 5 {
				snapshot, err, retry := manager.discover(t.Context(), nil, false)
				member, exists := snapshot.records[id]
				if err == nil || !retry || !snapshot.isComplete || !exists || member != nil {
					t.Errorf("failed member: complete=%t retry=%t exists=%t member=%+v err=%v", snapshot.isComplete, retry, exists, member, err)
				}
				store.commit(snapshot, err)
				if store.records[id] != previous || store.records[removed] != nil {
					t.Error("metadata failure advanced sync time or blocked confirmed deletion")
				}
			}
			failed = false
			snapshot, err, _ = manager.discover(t.Context(), nil, false)
			store.commit(snapshot, err)
			current := store.records[id]
			if err != nil || store.err != nil || calls != 8 || current.ref.Key != previous.ref.Key || current.pod.LifeResources("baseline") != 6400 {
				t.Errorf("recovery lost the instance or stopped retrying: calls=%d err=%v", calls, err)
			}
			if kata && !current.container.SyncedAt.Equal(previous.container.SyncedAt) {
				t.Error("Kata refresh changed representative priority")
			}
		})
	}
}

func TestContainerSourceIncompleteList(t *testing.T) {
	useContainerdForTest(t)
	id, retained, fresh := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	for _, reason := range []string{"duplicate", "cross pod duplicate", "missing spec", "invalid ID", "query failure"} {
		t.Run(reason, func(t *testing.T) {
			store := newTestContainerStore()
			source := containerSourceForTest(runningPodListForTest(id, retained).Items)
			manager := sourceManagerForTest(t, store, source)
			snapshot, err, _ := manager.discover(t.Context(), nil, true)
			if err != nil {
				t.Fatal(err)
			}
			store.commit(snapshot, nil)
			previous := store.records[id]
			list := runningPodListForTest(id, fresh)
			switch reason {
			case "duplicate":
				list = runningPodListForTest(id, id, fresh)
			case "cross pod duplicate":
				list.Items = append(list.Items, runningPodListForTest(id).Items...)
			case "missing spec":
				list.Items[0].Spec.Containers = list.Items[0].Spec.Containers[1:]
			case "invalid ID":
				list.Items[0].Status.ContainerStatuses[0].ContainerID = "containerd://invalid"
			}
			source.fetch = func(context.Context) (corev1.PodList, error) {
				if reason == "query failure" {
					return corev1.PodList{}, errors.New("kubelet unavailable")
				}
				return list, nil
			}
			snapshot, err, retry := manager.discover(t.Context(), nil, false)
			if err == nil || !retry || snapshot.isComplete {
				t.Errorf("incomplete list: complete=%t retry=%t err=%v", snapshot.isComplete, retry, err)
			}
			store.commit(snapshot, err)
			if store.records[id] != previous || store.records[retained] == nil {
				t.Error("incomplete list replaced conflicting metadata or deleted absent members")
			}
			if reason != "query failure" && store.records[fresh] == nil {
				t.Error("incomplete list discarded a valid member")
			}
		})
	}
}

func TestContainerSourceCancellation(t *testing.T) {
	useContainerdForTest(t)
	for _, phase := range []string{"fetch", "resolve"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			list := runningPodListForTest(strings.Repeat("a", 64))
			source := containerSourceForTest(list.Items)
			if phase == "fetch" {
				source.fetch = func(context.Context) (corev1.PodList, error) { cancel(); return corev1.PodList{}, nil }
			} else {
				source.resolve = func(context.Context, string, *corev1.Container, *corev1.ContainerStatus, *corev1.Pod) (*Pod, error) {
					cancel()
					return nil, nil
				}
			}
			discovery, err := source.Discover(ctx)
			if !errors.Is(err, context.Canceled) || discovery.Complete || len(discovery.Pods) != 0 {
				t.Errorf("canceled discovery=%+v err=%v", discovery, err)
			}
		})
	}
}

// Periodic refresh also handles runtime teardown before kubelet removes a row.
func TestContainerSourceReconcilesWithoutGetters(t *testing.T) {
	useContainerdForTest(t)
	id := strings.Repeat("a", 64)
	var mu sync.Mutex
	list := runningPodListForTest(id)
	source := containerSourceForTest(list.Items)
	source.fetch = func(context.Context) (corev1.PodList, error) { mu.Lock(); defer mu.Unlock(); return list, nil }
	store := newContainerStore()
	manager := sourceManagerForTest(t, store, source)
	manager.resyncInterval = 25 * time.Millisecond
	manager.start()
	t.Cleanup(func() { manager.cancel(); <-manager.done })
	subscriber := subscribeForTest(t, store)
	waitContainerViewForTest(t, subscriber, 1)
	mu.Lock()
	list = corev1.PodList{}
	mu.Unlock()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case <-subscriber.Notify():
		case <-timeout:
			t.Error("periodic deletion required a getter or CSS hint")
			return
		}
		update, err := subscriber.DrainEvents()
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range update.Events {
			if event.Kind == ContainerDeleted && event.Container.Key.ID == id {
				return
			}
		}
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

// TestKubeletInitializationCancellation 验证 configz 请求继承取消信号，
// 取消后不尝试本地配置兜底，不会将正常停止误判为配置缺失并 panic。
func TestKubeletInitializationCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := kubeletConfigCacheMustUpdate(ctx, &containerdiscovery.Kubelet{}); !errors.Is(err, context.Canceled) {
		t.Errorf("config initialization cancellation=%v", err)
	}
}
