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

package container

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
	podmodel "github.com/ccfos/huatuo/internal/pod"

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
	source := &containerSource{resolve: resolveContainerPod}
	source.fetch = func(ctx context.Context) (corev1.PodList, error) {
		return FetchPodList(ctx, server.Client(), server.URL)
	}

	if err := podmodel.StartManager(source, podmodel.ManagerOptions{SyncInterval: 25 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(podmodel.ReleaseManager)
	cached := waitContainerForTest(t, id)
	workload, _ := podmodel.PodByID(id)
	assertContainerSourceKernelPaths(t, workload, cached)
	ref, err := podmodel.ContainerRefByID(id)
	if err != nil || ref.InitPID != os.Getpid() {
		t.Fatalf("kernel reference=%+v err=%v", ref, err)
	}
	sub, err := podmodel.SubscribeContainers(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	// Runtime teardown and its CSS hint can precede kubelet's next status.
	if err := os.Remove(filepath.Join(runtimeDir, "init.pid")); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	deleting = true
	mu.Unlock()
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
		if errors.Is(err, podmodel.ErrContainersUnavailable) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if (update.Mode == podmodel.ContainerUpdateFull && len(update.Events) == 0) ||
			(len(update.Events) == 1 && update.Events[0].Kind == podmodel.ContainerDeleted && update.Events[0].Container.Key == ref.Key) {
			if cached, err := podmodel.ContainerByID(id); err != nil || cached != nil {
				t.Fatalf("deleted container remains available to the API: cached=%v, err=%v", cached, err)
			}
			return
		}
	}
}

// TestContainerSourceProductionStartup exercises source injection through real HTTPS and procfs.
func TestContainerSourceProductionStartup(t *testing.T) {
	useContainerdForTest(t)
	id := strings.Repeat("b", 64)
	runtimeRoot := t.TempDir()
	writePIDForTest(t, runtimeRoot, id, os.Getpid())
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
	previousRoot, previousClient := containerdStateDir, containerdClient
	previousDriver := kubeletPodCgroupDriver
	previousEndpoint, previousAPIVersion := kubeletRuntimeEndpoint, dockerAPIVersion
	t.Cleanup(func() {
		podmodel.ReleaseManager()
		containerdStateDir, containerdClient = previousRoot, previousClient
		kubeletPodCgroupDriver = previousDriver
		kubeletRuntimeEndpoint, dockerAPIVersion = previousEndpoint, previousAPIVersion
	})
	containerdStateDir = runtimeRoot
	response := runningRuntimeResponse(`{"runtimeType":"io.containerd.runc.v2"}`)
	response.Status.Id = id
	containerdClient = &pidRuntimeService{response: response}
	source, err := NewSource(&Options{PodAuthorizedPort: uint32(portNumber), PodClientCertPath: certPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := podmodel.StartManager(source, podmodel.ManagerOptions{}); err != nil {
		t.Error(err)
		return
	}
	cached := waitContainerForTest(t, id)
	workload, err := podmodel.PodByID(id)
	if err != nil || workload == nil {
		t.Fatalf("workload=%+v err=%v", workload, err)
	}
	assertContainerSourceKernelPaths(t, workload, cached)
	if cached.CgroupCss == nil {
		t.Error("empty legacy CSS must remain a JSON object")
	}
}

func assertContainerSourceKernelPaths(t *testing.T, workload *podmodel.Pod, container *podmodel.Container) {
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
		got, bound := workload.Cgroups.Path(controller)
		if want == "" || want == "/" {
			if bound {
				t.Errorf("%s bound the host root: %q", controller, got)
			}
			continue
		}
		if !bound || got != want {
			t.Errorf("%s path=%q bound=%t, kernel path=%q", controller, got, bound, want)
		}

	}
	if workload.Network == nil || workload.Network.PID != os.Getpid() || workload.Network.NamespaceInum == 0 {
		t.Error("container network namespace was not bound by the Manager")
	}
	if container.CgroupPath != "/kubepods/pod/"+workload.ID {
		t.Errorf("legacy kubelet path changed: %q", container.CgroupPath)
	}
}

func writePIDForTest(t testing.TB, root, id string, pid int) {
	t.Helper()
	file := filepath.Join(root, "io.containerd.runtime.v2.task", "k8s.io", id, "init.pid")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(strconv.Itoa(pid)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLocalRuntimeReferenceValidation(t *testing.T) {
	useContainerdForTest(t)
	previous := containerdStateDir
	containerdStateDir = t.TempDir()
	t.Cleanup(func() { containerdStateDir = previous })
	id := strings.Repeat("a", 64)
	writePIDForTest(t, containerdStateDir, id, os.Getpid())
	workload := containerPodForTest(id)
	workload.Process = &podmodel.HostProcess{PID: os.Getpid()}
	startSourceForTest(t, &sourceFixture{discover: func(context.Context) (podmodel.Discovery, error) {
		return podmodel.Discovery{Pods: map[string]*podmodel.Pod{id: workload}, Complete: true}, nil
	}})
	waitContainerForTest(t, id)
	ref, err := podmodel.ContainerRefByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateContainerRef(ref); err != nil {
		t.Fatal(err)
	}
	writePIDForTest(t, containerdStateDir, id, os.Getpid()+1)
	if err := ValidateContainerRef(ref); err == nil {
		t.Error("changed runtime PID was accepted")
	}
}
