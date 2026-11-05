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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/bpf/abi"
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
	store.commit(containerSnapshot{records: map[string]*containerRecord{"first": first}, isComplete: true}, nil)
	sub := subscribeForTest(t, store)
	// This change is covered by the initial full update, not also delivered as a delta.
	second := containerRecordForTest("second")
	records := map[string]*containerRecord{"first": first, "second": second}
	store.commit(containerSnapshot{records: records, isComplete: true}, nil)
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
	store.commit(containerSnapshot{records: records, isComplete: true}, nil)
	if !slices.Equal(initial.Events, borrowed) {
		t.Fatal("producer mutated borrowed view")
	}
	next, err := sub.DrainEvents()
	if err != nil || next.Mode != ContainerUpdateIncremental || len(next.Events) != 1 || next.Events[0].Kind != ContainerDeleted || next.Events[0].Container.Key != first.ref.Key || next.Revision <= revision {
		t.Fatalf("increment=%+v, %v", next, err)
	}

	store.commit(containerSnapshot{}, errors.New("kubelet query failed"))
	if _, err := sub.DrainEvents(); !errors.Is(err, ErrContainersUnavailable) {
		t.Fatal(err)
	}

	store.commit(containerSnapshot{records: records, isComplete: true}, nil)
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
	store.commit(containerSnapshot{records: map[string]*containerRecord{"first": first}, isComplete: true}, nil)
	slow, fast := subscribeForTest(t, store), subscribeForTest(t, store)
	_, _ = slow.DrainEvents()
	_, _ = fast.DrainEvents()
	records := make(map[string]*containerRecord)
	store.commit(containerSnapshot{records: records, isComplete: true}, nil)
	for i := 0; i < containerEventQueueSize+1; i++ {
		id := fmt.Sprint(i)
		records[id] = containerRecordForTest(id)
		store.commit(containerSnapshot{records: records, isComplete: true}, nil)
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
	store.commit(containerSnapshot{records: map[string]*containerRecord{"first": first}, isComplete: true}, nil)
	_, _ = sub.DrainEvents()
	store.commit(containerSnapshot{}, errors.New("kubelet query failed"))
	if update, err := sub.DrainEvents(); !errors.Is(err, ErrContainersUnavailable) || update.Mode != ContainerUpdateUnknown || len(store.records) != 1 {
		t.Fatal("failed sync deleted containers")
	}
	store.commit(containerSnapshot{isComplete: true}, nil)
	update, err := sub.DrainEvents()
	if err != nil || update.Mode != ContainerUpdateFull || len(update.Events) != 0 {
		t.Fatal("complete empty view did not produce a full update")
	}
}

func TestContainerSubscriptionInstanceGeneration(t *testing.T) {
	store := newTestContainerStore()
	first := containerRecordForTest("first")
	store.commit(containerSnapshot{records: map[string]*containerRecord{"first": first}, isComplete: true}, nil)
	sub := subscribeForTest(t, store)
	_, _ = sub.DrainEvents()
	metadata := containerRecordForTest("first")
	metadata.container.Name = "new label"
	store.commit(containerSnapshot{records: map[string]*containerRecord{"first": metadata}, isComplete: true}, nil)
	if metadata.ref.Key != first.ref.Key {
		t.Fatal("metadata update changed generation")
	}
	if update, _ := sub.DrainEvents(); len(update.Events) != 0 {
		t.Fatal("metadata update emitted lifecycle")
	}
	replacement := containerRecordForTest("first")
	replacement.startTime++
	store.commit(containerSnapshot{records: map[string]*containerRecord{"first": replacement}, isComplete: true}, nil)
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
	store.commit(containerSnapshot{records: records, isComplete: true}, nil)
	sub := subscribeForTest(t, store)
	full, err := sub.DrainEvents()
	if err != nil || full.Mode != ContainerUpdateFull || len(full.Events) != len(records) || cap(full.Events) != len(full.Events) {
		t.Fatalf("full update=%+v, %v", full, err)
	}

	// A large full update must not raise the incremental batch limit.
	store.commit(containerSnapshot{isComplete: true}, nil)
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
	containerView.commit(containerSnapshot{records: map[string]*containerRecord{"cached": record}, isComplete: true}, nil)
	containerView.commit(containerSnapshot{}, errors.New("kubelet temporarily unavailable"))
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
	store.commit(containerSnapshot{records: map[string]*containerRecord{record.ref.Key.ID: record}, isComplete: true}, nil)
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

// TestPodPublicationPreservesContainerContract 验证普通容器提交后的公共类型、
// 容器分类、进程和网络映射，以及旧 JSON 和同步时间保持不变；来源 map
// 的后续改动不能覆盖已经发布的标签、CSS 和独立 memory 路径。
func TestPodPublicationPreservesContainerContract(t *testing.T) {
	store := newTestContainerStore()
	record := containerRecordForTest("checkout-container")
	memory := "/memory/checkout"
	source := record.container
	source.Name, source.Hostname = "checkout", "huatuo-dev"
	source.CgroupPath, source.memoryCgroupPath = "/cpu/checkout", &memory
	source.CgroupCss = map[string]uint64{"cpu": 7100}
	source.Labels = map[string]any{labelHostNamespace: "checkout-services"}
	source.SyncedAt = time.Unix(100, 0)
	source.NetNamespaceInum, source.NetNamespaceCookie = 5100, 6100
	source.IPAddress = "172.16.0.2"
	wantJSON, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	store.commit(containerSnapshot{records: map[string]*containerRecord{source.ID: record}, isComplete: true}, nil)
	view := store.records[source.ID]
	if view.pod.Type != TypeContainer || view.pod.Container.Type != source.Type || view.pod.Container.Hostname != source.Hostname {
		t.Errorf("container classification changed: %+v", view.pod)
	}
	if view.pod.Process == nil || view.pod.Process.PID != source.InitPid {
		t.Errorf("process = %+v, want PID %d", view.pod.Process, source.InitPid)
	}
	if view.pod.Network == nil || view.pod.Network.NamespaceInum != source.NetNamespaceInum ||
		view.pod.Network.NamespaceCookie != source.NetNamespaceCookie || view.pod.Network.PID != source.InitPid {
		t.Errorf("network binding = %+v", view.pod.Network)
	}
	gotJSON, err := json.Marshal(view.container)
	if err != nil || !bytes.Equal(gotJSON, wantJSON) {
		t.Errorf("legacy JSON = %s, %v; want %s", gotJSON, err, wantJSON)
	}
	if !view.container.SyncedAt.Equal(source.SyncedAt) || !view.pod.SyncedAt.After(source.SyncedAt) {
		t.Errorf("sync timestamps: legacy=%s, pod=%s", view.container.SyncedAt, view.pod.SyncedAt)
	}
	source.Labels[labelHostNamespace] = "changed-at-source"
	source.CgroupCss["cpu"] = 7200
	memory = "/memory/changed-at-source"
	if view.pod.Labels[labelHostNamespace] != "checkout-services" || view.pod.Cgroups.CSS["cpu"] != 7100 ||
		view.container.MemoryCgroupPath() != "/memory/checkout" {
		t.Error("source metadata mutated a published view")
	}
}

// TestPodInstanceResources 验证采集状态只在实例创建时初始化，Pod 和旧容器入口
// 共享同一状态；元数据刷新保留状态与代次、旧视图不变，实例替换重新初始化，
// 完整空快照同时删除公共对象和兼容视图。
func TestPodInstanceResources(t *testing.T) {
	type usage struct{ Total uint64 }
	const resource = "test-checkout-usage"
	if err := RegisterContainerLifeResources(resource, reflect.TypeOf(&usage{})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lifeResourcesTpl.Delete(resource) })
	store := newTestContainerStore()
	first := containerRecordForTest("checkout-container")
	store.commit(containerSnapshot{records: map[string]*containerRecord{first.container.ID: first}, isComplete: true}, nil)
	initial := first.pod
	state, ok := initial.LifeResources(resource).(*usage)
	if !ok || state == nil {
		t.Fatal("initial instance has no usage state")
	}
	state.Total = 72
	if first.container.LifeResources(resource) != state {
		t.Error("legacy container does not share the Pod state")
	}
	metadata := containerRecordForTest(first.container.ID)
	metadata.container.Name = "checkout-renamed"
	store.commit(containerSnapshot{records: map[string]*containerRecord{first.container.ID: metadata}, isComplete: true}, nil)
	if metadata.ref.Key != first.ref.Key || metadata.pod.LifeResources(resource) != state || state.Total != 72 {
		t.Error("metadata update reset the instance or its usage baseline")
	}
	if initial.Name == metadata.pod.Name || initial == metadata.pod {
		t.Error("metadata update changed the previously published Pod")
	}
	replacement := containerRecordForTest(first.container.ID)
	replacement.startTime++
	store.commit(containerSnapshot{records: map[string]*containerRecord{first.container.ID: replacement}, isComplete: true}, nil)
	fresh, ok := replacement.pod.LifeResources(resource).(*usage)
	if !ok || fresh == nil || fresh == state || fresh.Total != 0 || replacement.ref.Key == first.ref.Key {
		t.Errorf("replacement retained old state: %+v", fresh)
	}
	if first.container.LifeResources(resource) != state || state.Total != 72 {
		t.Error("replacement mutated the old compatibility view")
	}
	store.commit(containerSnapshot{isComplete: true}, nil)
	if len(store.records) != 0 {
		t.Error("deleted workload retained a cached view")
	}
}

// TestPodQueriesKeepSandboxIdentity 验证仅有 cgroup 的沙箱可查询但不会进入旧
// 容器入口；发现失败和 nil 成员保留原对象与同步时间，返回集合可独立修改，
// 完整空列表删除对象，未启动 Manager 时查询返回空集。
func TestPodQueriesKeepSandboxIdentity(t *testing.T) {
	previous := containerView
	containerView = newTestContainerStore()
	t.Cleanup(func() { containerView = previous })
	source := &Pod{
		ID: "checkout-sandbox", Type: TypeCubeSandbox, Name: "checkout",
		Cgroups: CgroupBinding{Controllers: map[string]string{"memory": "/cube_sandbox/sandbox/8"}},
		Labels:  map[string]any{"workload_namespace": "checkout-services"},
	}
	record := &containerRecord{pod: source}
	containerView.commit(containerSnapshot{records: map[string]*containerRecord{source.ID: record}, isComplete: true}, nil)
	view, err := PodByID(source.ID)
	if err != nil || view == nil {
		t.Errorf("sandbox lookup = %+v, %v", view, err)
		return
	}
	if view.Type != TypeCubeSandbox || view.Process != nil || view.Network != nil || view.Container != nil {
		t.Errorf("invented sandbox resource or container metadata: %+v", view)
	}
	if legacy, lookupErr := ContainerByID(source.ID); lookupErr != nil || legacy != nil {
		t.Errorf("sandbox exposed as container: %+v, %v", legacy, lookupErr)
	}
	if legacy, lookupErr := Containers(); lookupErr != nil || len(legacy) != 0 {
		t.Errorf("sandbox exposed in container list: %+v, %v", legacy, lookupErr)
	}
	if legacy, lookupErr := ContainerRefByID(source.ID); lookupErr != nil || legacy != (ContainerRef{}) {
		t.Errorf("sandbox exposed as container reference: %+v, %v", legacy, lookupErr)
	}
	delete(source.Cgroups.Controllers, "memory")
	if view.Cgroups.Controllers["memory"] != "/cube_sandbox/sandbox/8" {
		t.Error("discovery metadata shared a mutable controller map")
	}
	containerView.commit(containerSnapshot{}, errors.New("cubelet unavailable"))
	containerView.commit(containerSnapshot{records: map[string]*containerRecord{source.ID: nil}, isComplete: true}, nil)
	retained, err := PodByID(source.ID)
	if err != nil || retained != view {
		t.Errorf("unresolved member replaced cached view: %+v, %v", retained, err)
	}
	all, err := Pods()
	if err != nil || all[source.ID] != view {
		t.Errorf("Pods() = %+v, %v", all, err)
	}
	delete(all, source.ID)
	if retained, _ := PodByID(source.ID); retained != view {
		t.Error("modifying query result removed a cached object")
	}
	containerView.commit(containerSnapshot{isComplete: true}, nil)
	if removed, _ := PodByID(source.ID); removed != nil {
		t.Error("complete empty discovery retained sandbox")
	}
	containerView = newContainerStore()
	if empty, lookupErr := Pods(); lookupErr != nil || len(empty) != 0 {
		t.Errorf("disabled Manager lookup = %+v, %v", empty, lookupErr)
	}
}

// TestPodSuccessfulRefreshPreservesLegacyView 验证复用已解析容器记录时发布新的
// Pod 同步时间，旧 Pod 和 Container 视图不变，不重复发送创建事件。
func TestPodSuccessfulRefreshPreservesLegacyView(t *testing.T) {
	store := newTestContainerStore()
	record := containerRecordForTest("checkout-container")
	record.container.SyncedAt = time.Unix(100, 0)
	store.commit(containerSnapshot{records: map[string]*containerRecord{record.container.ID: record}, isComplete: true}, nil)
	oldPod, oldContainer := record.pod, record.container
	oldTime := oldPod.SyncedAt
	sub := subscribeForTest(t, store)
	if _, err := sub.DrainEvents(); err != nil {
		t.Fatal(err)
	}
	store.commit(containerSnapshot{records: map[string]*containerRecord{record.container.ID: record}, isComplete: true}, nil)
	current := store.records[record.container.ID]
	if current.pod == oldPod || current.pod.SyncedAt.Before(oldTime) || !oldPod.SyncedAt.Equal(oldTime) {
		t.Error("successful refresh did not preserve immutable Pod views")
	}
	if current.container != oldContainer || current.ref != record.ref {
		t.Error("successful refresh changed the legacy view or instance reference")
	}
	if update, err := sub.DrainEvents(); err != nil || len(update.Events) != 0 {
		t.Errorf("successful refresh emitted lifecycle: %+v, %v", update, err)
	}
}

// TestPodPublicationKeepsBoundCgroups 验证已绑定的公共控制器路径不会被旧容器
// 的共享路径覆盖，兼容视图仍保留原字段，且来源修改不会影响已发布目录。
func TestPodPublicationKeepsBoundCgroups(t *testing.T) {
	store := newTestContainerStore()
	record := containerRecordForTest("checkout-container")
	record.container.CgroupPath = "/legacy-checkout"
	record.pod = podFromContainer(record.container)
	record.pod.Cgroups = CgroupBinding{
		Controllers: map[string]string{"cpu": "/checkout-cpu", "memory": "/checkout-memory"},
		directories: map[string]string{"cpu": "/mounted-cpu/checkout-cpu"},
	}
	source := record.pod
	store.commit(containerSnapshot{records: map[string]*containerRecord{source.ID: record}, isComplete: true}, nil)
	published := store.records[source.ID]
	if published.pod.Cgroups.Controllers["memory"] != "/checkout-memory" ||
		published.container.CgroupPath != "/legacy-checkout" {
		t.Error("legacy and public cgroup paths were conflated")
	}
	source.Cgroups.directories["cpu"] = "/changed-at-source"
	if published.pod.Cgroups.directories["cpu"] != "/mounted-cpu/checkout-cpu" {
		t.Error("source mutation changed a published directory")
	}
}

// TestPodFirstResourceReadiness 验证 PID、启动时间、内存目录、CSS、公共 cgroup、
// 进程及网络首次补齐时保留代次和采集基线，发送一次全量通知；重复提交、
// 名称或 IP 元数据更新不产生额外通知，已发布的旧视图保持不变。
func TestPodFirstResourceReadiness(t *testing.T) {
	directory, err := os.Stat(t.TempDir())
	if err != nil {
		t.Errorf("stat ready directory: %v", err)
		return
	}
	cases := []struct {
		name    string
		prepare func(*containerRecord, *containerRecord)
		notify  bool
	}{
		{name: "init PID", notify: true, prepare: func(before, after *containerRecord) { before.ref.InitPID = 0 }},
		{name: "process identity", notify: true, prepare: func(before, after *containerRecord) { before.startTime = 0 }},
		{name: "memory membership", notify: true, prepare: func(before, after *containerRecord) { before.ref.MemoryCgroupPath = "" }},
		{name: "memory directory", notify: true, prepare: func(before, after *containerRecord) { after.directory = directory }},
		{name: "CSS", notify: true, prepare: func(before, after *containerRecord) { after.pod.Cgroups.CSS = map[string]uint64{"memory": 7500} }},
		{name: "cgroup directory", notify: true, prepare: func(before, after *containerRecord) {
			after.pod.Cgroups.directories = map[string]string{"memory": "/bound-checkout"}
		}},
		{name: "process binding", notify: true, prepare: func(before, after *containerRecord) { before.pod.Process = nil }},
		{name: "network binding", notify: true, prepare: func(before, after *containerRecord) { after.pod.Network = &HostNetwork{PID: 42, NamespaceInum: 6500} }},
		{name: "IP metadata", prepare: func(before, after *containerRecord) { after.pod.Network = &HostNetwork{IPAddress: "172.16.0.2"} }},
		{name: "name metadata", prepare: func(before, after *containerRecord) { after.pod.Name = "checkout-worker" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestContainerStore()
			before, after := containerRecordForTest("checkout-container"), containerRecordForTest("checkout-container")
			before.pod, after.pod = podFromContainer(before.container), podFromContainer(after.container)
			tc.prepare(before, after)
			store.commit(containerSnapshot{records: map[string]*containerRecord{before.container.ID: before}, isComplete: true}, nil)
			baseline := &struct{ total uint64 }{total: 6400}
			before.pod.lifeResources["readiness-baseline"] = baseline
			subscriber := subscribeForTest(t, store)
			initial, err := subscriber.DrainEvents()
			if err != nil {
				t.Errorf("initial snapshot: %v", err)
				return
			}
			oldView := before.pod
			store.commit(containerSnapshot{records: map[string]*containerRecord{after.container.ID: after}, isComplete: true}, nil)
			if after.ref.Key != before.ref.Key || after.pod.LifeResources("readiness-baseline") != baseline {
				t.Error("first resource readiness replaced instance state")
			}
			if oldView == after.pod {
				t.Error("resource completion mutated the published view")
			}
			update, err := subscriber.DrainEvents()
			if err != nil {
				t.Errorf("resource readiness notification: %v", err)
				return
			}
			if tc.notify {
				if update.Mode != ContainerUpdateFull || len(update.Events) != 1 || update.Events[0].Container != after.ref || update.Revision <= initial.Revision {
					t.Errorf("readiness must produce a current full view: %+v", update)
				}
			} else if update.Mode != ContainerUpdateIncremental || len(update.Events) != 0 {
				t.Errorf("metadata emitted lifecycle notification: %+v", update)
			}
			store.commit(containerSnapshot{records: map[string]*containerRecord{after.container.ID: after}, isComplete: true}, nil)
			if duplicate, err := subscriber.DrainEvents(); err != nil || len(duplicate.Events) != 0 {
				t.Errorf("duplicate readiness notification: %+v, %v", duplicate, err)
			}
		})
	}
}

// TestPodSubscriptionIsolation 验证公共订阅返回容器和沙箱的独立身份，旧订阅
// 仅返回容器；首次快照、删除增量以及已借出数据的不可变性遵循同一队列规则。
func TestPodSubscriptionIsolation(t *testing.T) {
	store := newTestContainerStore()
	container := containerRecordForTest("checkout-container")
	sandbox := &containerRecord{pod: &Pod{ID: "checkout-sandbox", Type: TypeCubeSandbox, Name: "checkout"}}
	records := map[string]*containerRecord{container.container.ID: container, sandbox.pod.ID: sandbox}
	store.commit(containerSnapshot{records: records, isComplete: true}, nil)
	public, err := store.subscribePods(t.Context())
	if err != nil {
		t.Errorf("subscribe workloads: %v", err)
		return
	}
	t.Cleanup(public.Close)
	legacy := subscribeForTest(t, store)
	full, err := public.DrainEvents()
	if err != nil || full.Mode != PodUpdateFull || len(full.Events) != len(records) || full.DiscoveryError != nil {
		t.Errorf("workload snapshot=%+v, err=%v", full, err)
		return
	}
	for _, event := range full.Events {
		if event.Pod == nil || event.Key != records[event.Pod.ID].ref.Key || event.Kind != PodEventUnknown {
			t.Errorf("workload snapshot entry=%+v", event)
		}
	}
	old, err := legacy.DrainEvents()
	if err != nil || len(old.Events) != 1 || old.Events[0].Container != container.ref {
		t.Errorf("legacy projection=%+v, err=%v", old, err)
	}
	borrowed := slices.Clone(full.Events)
	delete(records, sandbox.pod.ID)
	store.commit(containerSnapshot{records: records, isComplete: true}, nil)
	if !slices.Equal(full.Events, borrowed) {
		t.Error("publication mutated a borrowed workload snapshot")
	}
	deleted, err := public.DrainEvents()
	if err != nil || deleted.Mode != PodUpdateIncremental || len(deleted.Events) != 1 || deleted.Events[0].Kind != PodDeleted || deleted.Events[0].Key != sandbox.ref.Key || deleted.Events[0].Pod.Type != TypeCubeSandbox {
		t.Errorf("sandbox delete=%+v, err=%v", deleted, err)
	}
	if update, err := legacy.DrainEvents(); err != nil || len(update.Events) != 0 {
		t.Errorf("sandbox leaked into legacy lifecycle: %+v, %v", update, err)
	}
}

// TestPodSubscriptionDiscoveryFailure 验证首次发现失败不能伪装成空快照；已有
// 非空或完整空缓存后，发现错误随快照返回，恢复后正常核对成员，旧接口仍返回错误。
func TestPodSubscriptionDiscoveryFailure(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprintf("empty=%t", empty), func(t *testing.T) {
			store := newTestContainerStore()
			store.err = ErrContainersUnavailable
			public, err := store.subscribePods(t.Context())
			if err != nil {
				t.Errorf("subscribe workloads: %v", err)
				return
			}
			t.Cleanup(public.Close)
			legacy := subscribeForTest(t, store)
			if update, err := public.DrainEvents(); !errors.Is(err, ErrPodsUnavailable) || update.Mode != PodUpdateUnknown {
				t.Errorf("initial failure became a snapshot: %+v, %v", update, err)
			}
			records := make(map[string]*containerRecord)
			if !empty {
				records["checkout-sandbox"] = &containerRecord{pod: &Pod{ID: "checkout-sandbox", Type: TypeCubeSandbox}}
			}
			store.commit(containerSnapshot{records: records, isComplete: true}, nil)
			_, _ = public.DrainEvents()
			_, _ = legacy.DrainEvents()
			failure := errors.New("cubelet request timed out")
			store.commit(containerSnapshot{}, failure)
			cached, err := public.DrainEvents()
			if err != nil || cached.Mode != PodUpdateFull || !errors.Is(cached.DiscoveryError, failure) || len(cached.Events) != len(records) {
				t.Errorf("cached snapshot during failure=%+v, err=%v", cached, err)
			}
			if _, err := legacy.DrainEvents(); !errors.Is(err, ErrContainersUnavailable) {
				t.Errorf("legacy error contract changed: %v", err)
			}
			store.commit(containerSnapshot{isComplete: true}, nil)
			recovered, err := public.DrainEvents()
			if err != nil || recovered.DiscoveryError != nil || recovered.Mode != PodUpdateFull || len(recovered.Events) != 0 {
				t.Errorf("recovery snapshot=%+v, err=%v", recovered, err)
			}
		})
	}
}

// TestPodSubscriptionReadinessAndClose 验证 cgroup-only 沙箱首次获得资源后
// 以同代次全量通知唤醒订阅者；关闭后借出结果不变，后续读取返回终止错误。
func TestPodSubscriptionReadinessAndClose(t *testing.T) {
	store := newTestContainerStore()
	pending := &containerRecord{pod: &Pod{ID: "checkout-sandbox", Type: TypeCubeSandbox}}
	store.commit(containerSnapshot{records: map[string]*containerRecord{pending.pod.ID: pending}, isComplete: true}, nil)
	public, err := store.subscribePods(t.Context())
	if err != nil {
		t.Errorf("subscribe workloads: %v", err)
		return
	}
	t.Cleanup(public.Close)
	_, _ = public.DrainEvents()
	ready := &containerRecord{pod: pending.pod.copyMetadata()}
	ready.pod.Cgroups.CSS = map[string]uint64{"memory": 8500}
	store.commit(containerSnapshot{records: map[string]*containerRecord{ready.pod.ID: ready}, isComplete: true}, nil)
	update, err := public.DrainEvents()
	if err != nil || update.Mode != PodUpdateFull || len(update.Events) != 1 || update.Events[0].Key != pending.ref.Key || update.Events[0].Pod.Cgroups.CSS["memory"] != 8500 {
		t.Errorf("resource readiness=%+v, err=%v", update, err)
	}
	borrowed := slices.Clone(update.Events)
	public.Close()
	if !slices.Equal(update.Events, borrowed) {
		t.Error("close mutated borrowed workload data")
	}
	if _, err := public.DrainEvents(); !errors.Is(err, ErrPodSubscriptionClosed) {
		t.Errorf("closed workload subscription=%v", err)
	}
}

// TestPodSubscriptionQueuedIdentity 验证队列保存发布时的代次和值，而不是可被
// 后续复用的记录指针；创建和删除通知按各自发布时的身份交付。
func TestPodSubscriptionQueuedIdentity(t *testing.T) {
	store := newTestContainerStore()
	public, err := store.subscribePods(t.Context())
	if err != nil {
		t.Errorf("subscribe workloads: %v", err)
		return
	}
	t.Cleanup(public.Close)
	_, _ = public.DrainEvents()
	record := &containerRecord{pod: &Pod{ID: "checkout-sandbox", Type: TypeCubeSandbox}, ref: ContainerRef{Key: PodKey{ID: "checkout-sandbox", Generation: 1}}}
	store.mu.Lock()
	store.publish(PodCreated, record)
	record.ref.Key.Generation++
	store.publish(PodDeleted, record)
	store.mu.Unlock()
	update, err := public.DrainEvents()
	if err != nil || len(update.Events) != 2 {
		t.Errorf("queued lifecycle=%+v, err=%v", update, err)
		return
	}
	if update.Events[0].Key.Generation != 1 || update.Events[1].Key.Generation != 2 {
		t.Errorf("queue retained a mutable record identity: %+v", update.Events)
	}
}

// TestPodCgroupLifecycle 验证数字目录按已验证内核关联归属；v1 删除仅使对应
// 控制器失效，v2 删除使统一绑定失效。来源失联时仍通知资源失效，不删除对象，
// 恢复绑定保留代次和采集状态，完整空快照清除反向索引。
func TestPodCgroupLifecycle(t *testing.T) {
	for _, unified := range []bool{false, true} {
		t.Run(fmt.Sprintf("unified=%t", unified), func(t *testing.T) {
			store := newTestContainerStore()
			cpuDirectory, memoryDirectory := t.TempDir(), t.TempDir()
			binding := CgroupBinding{
				Controllers:  map[string]string{"cpu": "/cube_sandbox/sandbox/8", "memory": "/cube_sandbox/sandbox/8"},
				directories:  map[string]string{"cpu": cpuDirectory, "memory": memoryDirectory},
				CSS:          map[string]uint64{"cpu": 7501, "memory": 7502},
				kernelGroups: map[string]uint64{"cpu": 9501, "memory": 9502},
			}
			if unified {
				binding.Controllers = nil
				binding.Unified = "/cube_sandbox/sandbox/8"
				binding.directories = map[string]string{"": cpuDirectory}
				binding.kernelGroups["memory"] = 9501
			}
			record := &containerRecord{pod: &Pod{ID: "checkout-sandbox", Type: TypeCubeSandbox, Cgroups: binding}}
			store.commit(containerSnapshot{records: map[string]*containerRecord{record.pod.ID: record}, isComplete: true}, nil)
			baseline := &struct{ value uint64 }{value: 6400}
			record.pod.lifeResources["lifecycle-baseline"] = baseline
			subscriber, err := store.subscribePods(t.Context())
			if err != nil {
				t.Errorf("subscribe workloads: %v", err)
				return
			}
			t.Cleanup(subscriber.Close)
			_, _ = subscriber.DrainEvents()
			store.commit(containerSnapshot{}, errors.New("cubelet offline"))
			_, _ = subscriber.DrainEvents()
			event := &containerCssPerfEvent{Operation: abi.CgroupCSSOperationUpdate, Cgroup: 9501}
			copy(event.KnodeName[:], "8")
			if id := store.cgroupEvent(event); id != record.pod.ID {
				t.Errorf("numeric cgroup owner=%q", id)
			}
			event.Operation = abi.CgroupCSSOperationRemove
			if id := store.cgroupEvent(event); id != record.pod.ID {
				t.Errorf("removed cgroup owner=%q", id)
			}
			current := store.records[record.pod.ID]
			if current == nil || current.ref.Key != record.ref.Key || current.pod.LifeResources("lifecycle-baseline") != baseline {
				t.Error("cgroup deletion removed or replaced the workload")
				return
			}
			if _, err := current.pod.Cgroups.Directory("cpu"); err == nil || current.pod.Cgroups.CSS["cpu"] != 0 {
				t.Error("removed CPU binding remains usable")
			}
			_, memoryErr := current.pod.Cgroups.Directory("memory")
			if (memoryErr != nil) != unified {
				t.Errorf("memory availability after CPU deletion=%v", memoryErr)
			}
			if record.pod.Cgroups.CSS["cpu"] != 7501 {
				t.Error("invalidation mutated the previously published view")
			}
			update, err := subscriber.DrainEvents()
			if err != nil || update.DiscoveryError == nil || update.Mode != PodUpdateFull || len(update.Events) != 1 {
				t.Errorf("resource invalidation during outage=%+v, %v", update, err)
			}
			restored := &containerRecord{pod: record.pod.copyMetadata()}
			store.commit(containerSnapshot{records: map[string]*containerRecord{record.pod.ID: restored}, isComplete: true}, nil)
			if restored.ref.Key != record.ref.Key || restored.pod.LifeResources("lifecycle-baseline") != baseline {
				t.Error("resource recovery reset workload state")
			}
			event.Operation = abi.CgroupCSSOperationUpdate
			if id := store.cgroupEvent(event); id != record.pod.ID {
				t.Errorf("recovered resource index owner=%q", id)
			}
			store.commit(containerSnapshot{isComplete: true}, nil)
			if len(store.cgroupOwners)+len(store.cssOwners) != 0 {
				t.Error("confirmed deletion retained resource owners")
			}
		})
	}
}

// TestSharedCgroupInvalidation 验证多个容器共享 VM cgroup 时全部绑定同时失效，
// 不选择任意容器归属；旧容器 CSS 视图更新，成员和各自实例代次仍保留。
func TestSharedCgroupInvalidation(t *testing.T) {
	store := newTestContainerStore()
	records := make(map[string]*containerRecord)
	for _, id := range []string{"checkout-api", "checkout-worker"} {
		record := containerRecordForTest(id)
		record.container.RuntimeType, record.container.SandboxID = "io.containerd.kata-qemu.v2", "checkout-vm"
		record.container.CgroupCss = map[string]uint64{"cpu": 8500}
		record.pod = podFromContainer(record.container)
		record.pod.Cgroups = CgroupBinding{CSS: map[string]uint64{"cpu": 8500}, directories: map[string]string{"cpu": "/bound-checkout"}}
		records[id] = record
	}
	store.commit(containerSnapshot{records: records, isComplete: true}, nil)
	event := &containerCssPerfEvent{Operation: abi.CgroupCSSOperationRemove}
	event.CSS[0] = 8500
	if id := store.cgroupEvent(event); id != "" {
		t.Errorf("shared resource arbitrarily assigned to %q", id)
	}
	for id, previous := range records {
		current := store.records[id]
		if current == nil || current.ref.Key != previous.ref.Key || len(current.pod.Cgroups.CSS) != 0 || len(current.container.CgroupCss) != 0 {
			t.Errorf("shared resource invalidation failed for %s: %+v", id, current)
		}
	}
}

// TestUnboundCgroupIsNotIndexed 验证未经目录绑定的 CSS 以及未知事件不会进入
// 归属索引或改变对象成员，目录名称不能冒充 workload ID。
func TestUnboundCgroupIsNotIndexed(t *testing.T) {
	store := newTestContainerStore()
	record := &containerRecord{pod: &Pod{ID: "checkout-sandbox", Type: TypeCubeSandbox, Cgroups: CgroupBinding{CSS: map[string]uint64{"cpu": 8500}}}}
	store.commit(containerSnapshot{records: map[string]*containerRecord{record.pod.ID: record}, isComplete: true}, nil)
	event := &containerCssPerfEvent{Operation: abi.CgroupCSSOperationRemove}
	event.CSS[0] = 8500
	copy(event.KnodeName[:], record.pod.ID)
	if id := store.cgroupEvent(event); id != "" || len(store.cgroupOwners)+len(store.cssOwners) != 0 || len(store.records) != 1 {
		t.Errorf("unbound resource changed ownership: id=%q", id)
	}
	ready := *record
	ready.pod = record.pod.copyMetadata()
	ready.pod.Cgroups.directories = map[string]string{"cpu": "/bound-checkout"}
	store.commit(containerSnapshot{records: map[string]*containerRecord{ready.pod.ID: &ready}, isComplete: true}, nil)
	event.Operation = abi.CgroupCSSOperationUpdate
	if id := store.cgroupEvent(event); id != record.pod.ID {
		t.Errorf("first directory binding did not index existing CSS: id=%q", id)
	}
	unbound := ready
	unbound.pod = ready.pod.copyMetadata()
	unbound.pod.Cgroups.directories = nil
	store.commit(containerSnapshot{records: map[string]*containerRecord{unbound.pod.ID: &unbound}, isComplete: true}, nil)
	if id := store.cgroupEvent(event); id != "" || len(store.cgroupOwners)+len(store.cssOwners) != 0 {
		t.Errorf("lost directory binding retained CSS ownership: id=%q", id)
	}
}
