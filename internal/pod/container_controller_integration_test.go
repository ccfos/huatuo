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
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/cgroups"
	containerdiscovery "github.com/ccfos/huatuo/internal/pod/container"

	corev1 "k8s.io/api/core/v1"
)

// TestContainerControllerHTTPAndKernelBinding 验证 HTTP 发现、CRI 运行时分类和
// 真实 /proc/cgroup 绑定，并覆盖 PID 文件先消失、kubelet 状态随后删除的顺序。
// CSS 探针和 memorywatch 内核事件由各自的集成测试覆盖。
func TestContainerControllerHTTPAndKernelBinding(t *testing.T) {
	useContainerdForTest(t)
	id := strings.Repeat("a", 64)
	runtimeRoot := t.TempDir()
	runtimeDir := filepath.Join(runtimeRoot, "io.containerd.runtime.v2.task", "k8s.io", id)
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "init.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	initMu.Lock()
	previousRoot := containerdStateDir
	previousClient := containerdClient
	containerdStateDir = runtimeRoot
	response := runningRuntimeResponse(`{"runtimeType":"io.containerd.runc.v2"}`)
	response.Status.Id = id
	containerdClient = &pidRuntimeService{response: response}
	initMu.Unlock()
	t.Cleanup(func() {
		initMu.Lock()
		containerdStateDir, containerdClient = previousRoot, previousClient
		initMu.Unlock()
	})
	var mu sync.Mutex
	list := runningPodListForTest(id)
	list.Items[0].Status.Phase = corev1.PodRunning
	deleting := false
	staleFetched := make(chan struct{})
	var staleOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if err := json.NewEncoder(w).Encode(list); err != nil {
			t.Error(err)
		}
		if deleting && len(list.Items) != 0 {
			staleOnce.Do(func() { close(staleFetched) })
		}
	}))
	t.Cleanup(server.Close)
	store := newContainerStore()
	previousView := containerView
	containerView = store
	t.Cleanup(func() { containerView = previousView })
	source := &containerSource{resolve: resolveContainerPod}
	controller := sourceManagerForTest(t, store, source)
	controller.resyncInterval = 25 * time.Millisecond
	source.fetch = func(ctx context.Context) (corev1.PodList, error) {
		return containerdiscovery.FetchPodList(ctx, server.Client(), server.URL)
	}

	controller.start()
	t.Cleanup(func() { controller.cancel(); <-controller.done })
	sub := subscribeForTest(t, store)
	waitContainerViewForTest(t, sub, 1)
	store.mu.RLock()
	record := store.records[id]
	store.mu.RUnlock()
	if record.ref.InitPID != os.Getpid() || record.startTime == 0 {
		t.Fatal("kernel binding missing")
	}
	assertContainerSourceKernelPaths(t, record)
	// Runtime teardown and its CSS hint can precede kubelet's next status.
	if err := os.Remove(filepath.Join(runtimeDir, "init.pid")); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	deleting = true
	mu.Unlock()
	controller.request(id, true)
	select {
	case <-staleFetched:
	case <-time.After(2 * time.Second):
		t.Fatal("deletion hint did not fetch the stale kubelet status")
	}
	mu.Lock()
	list = corev1.PodList{}
	mu.Unlock()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case <-sub.Notify():
		case <-timeout:
			t.Fatal("HTTP deletion did not reach subscription")
		}
		update, err := sub.DrainEvents()
		if errors.Is(err, ErrContainersUnavailable) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if (update.Mode == ContainerUpdateFull && len(update.Events) == 0) ||
			(len(update.Events) == 1 && update.Events[0].Kind == ContainerDeleted && update.Events[0].Container.Key == record.ref.Key) {
			if cached, err := ContainerByID(id); err != nil || cached != nil {
				t.Fatalf("deleted container remains available to the API: cached=%v, err=%v", cached, err)
			}
			return
		}
	}
}

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
	source := &containerSource{resolve: resolveContainerPod}
	controller := sourceManagerForTest(t, store, source)
	controller.discoveryTimeout = 30 * time.Millisecond
	source.fetch = func(ctx context.Context) (corev1.PodList, error) {
		return containerdiscovery.FetchPodList(ctx, server.Client(), server.URL)
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

// TestContainerManagerWithoutCSS exercises the production startup path using
// real HTTP and procfs while CSS initialization fails before loading BPF.
func TestContainerManagerWithoutCSS(t *testing.T) {
	useContainerdForTest(t)
	id := strings.Repeat("b", 64)
	runtimeRoot := t.TempDir()
	guestFile(t, runtimeRoot, filepath.Join("io.containerd.runtime.v2.task", "k8s.io", id, "init.pid"), strconv.Itoa(os.Getpid()))
	list := runningPodListForTest(id)
	list.Items[0].Status.Phase = corev1.PodRunning
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/configz" {
			if err := json.NewEncoder(w).Encode(map[string]any{"kubeletconfig": map[string]string{"cgroupDriver": "cgroupfs"}}); err != nil {
				t.Error(err)
			}
			return
		}
		if err := json.NewEncoder(w).Encode(list); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Error(err)
		return
	}
	portNumber, err := strconv.ParseUint(port, 10, 32)
	if err != nil {
		t.Error(err)
		return
	}
	certificate := server.TLS.Certificates[0]
	key, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if err != nil {
		t.Error(err)
		return
	}
	certPath := filepath.Join(t.TempDir(), "client.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]})
	certPEM = append(certPEM, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})...)
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Error(err)
		return
	}
	previousView, previousLoader := containerView, cgroupSubSysLoader
	previousIDs := cgroupCssID2SubSysNameMap
	previousRoot, previousClient := containerdStateDir, containerdClient
	previousDriver := kubeletPodCgroupDriver
	previousEndpoint, previousAPIVersion := kubeletRuntimeEndpoint, dockerAPIVersion
	t.Cleanup(func() {
		ReleaseManager()
		containerView, cgroupSubSysLoader = previousView, previousLoader
		cgroupCssID2SubSysNameMap = previousIDs
		containerdStateDir, containerdClient = previousRoot, previousClient
		kubeletPodCgroupDriver = previousDriver
		kubeletRuntimeEndpoint, dockerAPIVersion = previousEndpoint, previousAPIVersion
	})
	containerView = newContainerStore()
	containerdStateDir = runtimeRoot
	response := runningRuntimeResponse(`{"runtimeType":"io.containerd.runc.v2"}`)
	response.Status.Id = id
	containerdClient = &pidRuntimeService{response: response}
	cgroupCssID2SubSysNameMap = map[int]string{}
	attempted := make(chan struct{}, 1)
	cgroupSubSysLoader = func() error {
		attempted <- struct{}{}
		return errors.New("test kernel has no CSS probe support")
	}
	source, err := NewContainerSource(&ContainerSourceOptions{PodAuthorizedPort: uint32(portNumber), PodClientCertPath: certPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := StartManager(source, ManagerOptions{}); err != nil {
		t.Error(err)
		return
	}
	subscriber := subscribeForTest(t, containerView)
	waitContainerViewForTest(t, subscriber, 1)
	select {
	case <-attempted:
	default:
		t.Error("startup did not attempt CSS initialization")
	}
	containerView.mu.RLock()
	record := containerView.records[id]
	containerView.mu.RUnlock()
	if record == nil || record.container == nil || record.pod == nil {
		t.Error("CSS failure prevented container discovery")
		return
	}
	if record.ref.InitPID != os.Getpid() || record.pod.Process == nil {
		t.Error("CSS failure prevented kernel resource binding")
	}
	assertContainerSourceKernelPaths(t, record)
	if record.container.CgroupCss == nil {
		t.Error("legacy empty CSS must remain a JSON object")
	}
}

func assertContainerSourceKernelPaths(t *testing.T, record *containerRecord) {
	t.Helper()
	paths, err := cgroups.PathsForPID(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	for _, controller := range []string{"cpu", "memory"} {
		want := paths.Controllers[controller]
		if cgroups.CgroupMode() == cgroups.Unified {
			want = paths.Unified
		}
		got, bound := record.pod.Cgroups.Path(controller)
		if want == "" || want == "/" {
			if bound {
				t.Errorf("%s bound the host root: %q", controller, got)
			}
			continue
		}
		if !bound || got != want {
			t.Errorf("%s path=%q bound=%t, kernel path=%q", controller, got, bound, want)
		}
		if controller == "memory" && (record.directory == nil || record.ref.MemoryCgroupPath != want) {
			t.Error("memory instance identity was not bound to the kernel path")
		}
	}
	if record.pod.Network == nil || record.pod.Network.PID != os.Getpid() || record.pod.Network.NamespaceInum == 0 {
		t.Error("container network namespace was not bound by the Manager")
	}
	if record.container.CgroupPath != "/kubepods/pod/"+record.pod.ID {
		t.Errorf("legacy kubelet path changed: %q", record.container.CgroupPath)
	}
}
