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

//go:build integration && !didi

package pod

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/cgroups"
)

// TestWorkloadManagerHTTPDeadline 使用真实 HTTP 连接验证整轮超时会取消正在
// 等待的 kubelet 请求，不发布完整空列表，也不留下阻塞停止的后台查询。
func TestWorkloadManagerHTTPDeadline(t *testing.T) {
	canceled := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
		select {
		case canceled <- struct{}{}:
		default:
		}
	}))
	t.Cleanup(server.Close)
	store := newContainerStore()
	source := &sourceFixture{kind: TypeContainer}
	controller := sourceManagerForTest(t, store, source)
	controller.discoveryTimeout = 30 * time.Millisecond
	source.discover = func(ctx context.Context) (Discovery, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
		if err != nil {
			return Discovery{}, err
		}
		response, err := server.Client().Do(request)
		if err != nil {
			return Discovery{}, err
		}
		defer response.Body.Close()
		return Discovery{Complete: true}, nil
	}
	controller.start()
	t.Cleanup(func() { controller.cancel(); <-controller.done })
	subscriber := subscribeForTest(t, store)
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Error("discovery deadline did not cancel the HTTP request")
		return
	}
	deadline := time.After(time.Second)
	for {
		select {
		case <-subscriber.Notify():
		case <-deadline:
			t.Error("HTTP timeout was not reported to subscribers")
			return
		}
		_, err := subscriber.DrainEvents()
		if errors.Is(err, context.DeadlineExceeded) {
			break
		}
	}
	store.mu.RLock()
	complete, count := store.hasSnapshot, len(store.records)
	store.mu.RUnlock()
	if complete || count != 0 {
		t.Error("HTTP timeout published an authoritative snapshot")
	}
}

// TestSourceCgroupBindingIntegration 验证公共 Source 到真实 cgroup 文件的链路。
// 测试仅创建独立空组，不迁移进程；资源消失保留实例，来源确认空集才删除。
func TestSourceCgroupBindingIntegration(t *testing.T) {
	membership := "/huatuo-source-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	locations := CgroupBinding{Controllers: map[string]string{"cpu": membership, "memory": membership}}
	roots := []string{cgroups.RootFsFilePath("cpu"), cgroups.RootFsFilePath("memory")}
	if cgroups.CgroupMode() == cgroups.Unified {
		locations = CgroupBinding{Unified: membership}
		roots = []string{cgroups.RootfsDefaultPath()}
	}
	directories := make(map[string]struct{})
	for _, root := range roots {
		root, err := filepath.EvalSymlinks(root)
		if err != nil {
			t.Errorf("resolve cgroup mount: %v", err)
			return
		}
		directory := filepath.Join(root, membership)
		if _, created := directories[directory]; created {
			continue
		}
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Errorf("create isolated cgroup: %v", err)
			return
		}
		directories[directory] = struct{}{}
		t.Cleanup(func() {
			if err := os.Remove(directory); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Errorf("remove isolated cgroup: %v", err)
			}
		})
	}
	raw := &Pod{ID: "checkout-sandbox", Type: TypeCubeSandbox, StartedAt: time.Unix(80, 0), Cgroups: locations}
	raw.Cgroups.CSS = map[string]uint64{"cpu": 1}
	discovery := Discovery{Pods: map[string]*Pod{raw.ID: raw}, Complete: true, GuestPeers: map[string]GuestPeer{raw.ID: {PID: os.Getpid()}}}
	source := &sourceFixture{kind: TypeCubeSandbox, discover: func(context.Context) (Discovery, error) { return discovery, nil }}
	store := newTestContainerStore()
	manager, err := newSourceManager(store, source, ManagerOptions{})
	if err != nil {
		t.Error(err)
		return
	}
	commit := func() {
		snapshot, err, _ := manager.discover(t.Context(), nil, true)
		if err != nil {
			t.Error(err)
		}
		store.commit(snapshot, err)
	}
	commit()
	record := store.records[raw.ID]
	if record == nil {
		t.Error("source did not publish workload")
		return
	}
	for _, controller := range []string{"cpu", "memory"} {
		directory, err := record.pod.Cgroups.Directory(controller)
		if err != nil {
			t.Errorf("bind %s: %v", controller, err)
			continue
		}
		if _, err := os.Stat(filepath.Join(directory, controller+".stat")); err != nil {
			t.Errorf("read %s metrics location: %v", controller, err)
		}
	}
	if record.pod.Cgroups.CSS["cpu"] == 1 || record.pod.Process != nil || record.pod.Network != nil {
		t.Error("source CSS or peer became a collection binding")
	}
	record.pod.lifeResources["baseline"] = uint64(6400)
	for directory := range directories {
		if err := os.Remove(directory); err != nil {
			t.Errorf("remove bound cgroup: %v", err)
			return
		}
	}
	commit()
	current := store.records[raw.ID]
	if current.ref.Key != record.ref.Key || current.pod.LifeResources("baseline") != uint64(6400) {
		t.Error("resource disappearance replaced the instance")
	}
	if _, err := current.pod.Cgroups.Directory("cpu"); err == nil {
		t.Error("removed cgroup remains a collection target")
	}
	discovery = Discovery{Complete: true}
	commit()
	if len(store.records) != 0 || len(store.cgroupOwners)+len(store.cssOwners) != 0 {
		t.Error("complete empty discovery retained workload state")
	}
}

func TestStartManagerWithoutCSS(t *testing.T) {
	previousView, previousLoader := containerView, cgroupSubSysLoader
	previousIDs := cgroupCssID2SubSysNameMap
	containerView = newContainerStore()
	cgroupCssID2SubSysNameMap = map[int]string{}
	cgroupSubSysLoader = func() error { return errors.New("test kernel has no CSS support") }
	t.Cleanup(func() {
		ReleaseManager()
		containerView = previousView
		cgroupSubSysLoader = previousLoader
		cgroupCssID2SubSysNameMap = previousIDs
	})
	source := &sourceFixture{kind: TypeContainer, discover: func(context.Context) (Discovery, error) {
		return Discovery{Pods: map[string]*Pod{"checkout": containerPodForTest("checkout")}, Complete: true}, nil
	}}
	if err := StartManager(source, ManagerOptions{}); err != nil {
		t.Fatal(err)
	}
	sub := subscribeForTest(t, containerView)
	waitContainerViewForTest(t, sub, 1)
	container, err := ContainerByID("checkout")
	if err != nil || container == nil || container.CgroupCss == nil {
		t.Errorf("CSS failure blocked metadata: %+v %v", container, err)
	}
}
