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

package container

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/paths"
	podmodel "github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/procfs"

	criapi "k8s.io/cri-api/pkg/apis"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

const testContainerID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestContainerInitPIDInDockerRoot(t *testing.T) {
	rootDir := t.TempDir()
	containerDir := filepath.Join(rootDir, "containers", testContainerID)
	if err := os.MkdirAll(containerDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	config := []byte(`{"State":{"Pid":1234}}`)
	if err := os.WriteFile(filepath.Join(containerDir, "config.v2.json"), config, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	pid, err := ReadDockerPID(rootDir, testContainerID[:12])
	if err != nil {
		t.Fatalf("ReadDockerPID() error = %v", err)
	}
	if pid != 1234 {
		t.Fatalf("ReadDockerPID() = %d, want 1234", pid)
	}
}

func TestContainerInitPIDInContainerdState(t *testing.T) {
	stateDir := t.TempDir()
	containerDir := filepath.Join(
		stateDir,
		"io.containerd.runtime.v2.task",
		"k8s.io",
		testContainerID,
	)
	if err := os.MkdirAll(containerDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(containerDir, "init.pid"), []byte("5678\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	pid, err := ReadContainerdPID(stateDir, testContainerID[:12])
	if err != nil {
		t.Fatalf("ReadContainerdPID() error = %v", err)
	}
	if pid != 5678 {
		t.Fatalf("ReadContainerdPID() = %d, want 5678", pid)
	}
}

func TestResolveContainerIDRejectsAmbiguousPrefix(t *testing.T) {
	parentDir := t.TempDir()
	for _, id := range []string{
		"0123456789abaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"0123456789abbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	} {
		if err := os.Mkdir(filepath.Join(parentDir, id), 0o755); err != nil {
			t.Fatalf("Mkdir() error = %v", err)
		}
	}

	_, err := resolveContainerID(parentDir, "0123456789ab")
	if err == nil {
		t.Fatal("resolveContainerID() returned nil error for ambiguous prefix")
	}
}

type pidRuntimeService struct {
	criapi.RuntimeService
	response     *runtimeapi.ContainerStatusResponse
	err          error
	calls        int
	containerID  string
	verbose      bool
	checkContext bool
}

func (s *pidRuntimeService) ContainerStatus(
	ctx context.Context,
	id string,
	verbose bool,
) (*runtimeapi.ContainerStatusResponse, error) {
	s.calls++
	s.containerID = id
	s.verbose = verbose
	if s.checkContext {
		return nil, ctx.Err()
	}
	return s.response, s.err
}

func runningRuntimeResponse(info string) *runtimeapi.ContainerStatusResponse {
	return &runtimeapi.ContainerStatusResponse{
		Status: &runtimeapi.ContainerStatus{Id: "test", State: runtimeapi.ContainerState_CONTAINER_RUNNING},
		Info:   map[string]string{"info": info},
	}
}

func TestReadContainerdRuntimeInfo(t *testing.T) {
	exited := runningRuntimeResponse(`{"runtimeType":"io.containerd.kata.v2"}`)
	exited.Status.State = runtimeapi.ContainerState_CONTAINER_EXITED
	wrongID := runningRuntimeResponse(`{"runtimeType":"io.containerd.kata.v2"}`)
	wrongID.Status.Id = "another"
	for _, tt := range []struct {
		name     string
		response *runtimeapi.ContainerStatusResponse
		err      error
		wantErr  string
	}{
		{
			name:     "Kata",
			response: runningRuntimeResponse(`{"pid":1234,"runtimeType":"io.containerd.kata-qemu.v2","sandboxID":"sandbox"}`),
		},
		{
			name:     "runc",
			response: runningRuntimeResponse(`{"pid":1234,"runtimeType":"io.containerd.runc.v2","sandboxID":"sandbox"}`),
		},
		{name: "missing runtime", response: runningRuntimeResponse(`{"pid":1234}`), wantErr: "no CRI runtime type"},
		{name: "missing info", response: runningRuntimeResponse(""), wantErr: "decode containerd container info"},
		{name: "bad JSON", response: runningRuntimeResponse("invalid"), wantErr: "decode containerd container info"},
		{name: "exited", response: exited, wantErr: "no matching running CRI status"},
		{name: "wrong ID", response: wrongID, wantErr: "no matching running CRI status"},
		{name: "nil response", wantErr: "no matching running CRI status"},
		{name: "nil status", response: &runtimeapi.ContainerStatusResponse{}, wantErr: "no matching running CRI status"},
		{name: "CRI error", err: errors.New("runtime unavailable"), wantErr: "runtime unavailable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &pidRuntimeService{response: tt.response, err: tt.err}
			info, err := ReadContainerdRuntimeInfo(t.Context(), client, "test")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %v, want %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			} else if info.PID != 1234 || info.SandboxID != "sandbox" || info.RuntimeType == "" {
				t.Errorf("runtime info = %+v", info)
			}
			if client.calls != 1 || client.containerID != "test" || !client.verbose {
				t.Errorf("unexpected CRI request: %+v", client)
			}
		})
	}
	if _, err := ReadContainerdRuntimeInfo(t.Context(), nil, "test"); err == nil {
		t.Error("uninitialized CRI client must fail")
	}
}

func TestKubeletUpdateContainerRuntimeRouting(t *testing.T) {
	oldClient, oldProvider := containerdClient, currContainerProvider
	oldDir, oldDriver := containerdStateDir, kubeletPodCgroupDriver
	t.Cleanup(func() {
		containerdClient, currContainerProvider = oldClient, oldProvider
		containerdStateDir, kubeletPodCgroupDriver = oldDir, oldDriver
	})
	currContainerProvider = containerProviderContainerd
	kubeletPodCgroupDriver = "systemd"
	containerdStateDir = t.TempDir()
	pidFile := filepath.Join(containerdStateDir, "io.containerd.runtime.v2.task", "k8s.io", "test", "init.pid")
	if err := os.MkdirAll(filepath.Dir(pidFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pidFile, []byte(fmt.Sprint(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "test-ns", UID: "pod-uid"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, QOSClass: corev1.PodQOSBurstable},
	}
	status := &corev1.ContainerStatus{
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(time.Now())}},
	}
	for _, tt := range []struct{ name, info, wantErr string }{
		{name: "runc uses pid file", info: `{"pid":999999,"runtimeType":"io.containerd.runc.v2"}`},
		{
			name:    "Kata ignores existing pid file",
			info:    `{"pid":0,"runtimeType":"io.containerd.kata-qemu.v2","sandboxID":"sandbox"}`,
			wantErr: "invalid Kata hypervisor pid",
		},
		{
			name:    "Kata needs sandbox ID",
			info:    `{"pid":1234,"runtimeType":"io.containerd.kata.v2"}`,
			wantErr: "has no sandbox ID",
		},
		{
			name:    "Kata process has exited",
			info:    `{"pid":2147483647,"runtimeType":"io.containerd.kata.v2","sandboxID":"sandbox"}`,
			wantErr: "resolve Kata hypervisor cgroup",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &pidRuntimeService{response: runningRuntimeResponse(tt.info)}
			containerdClient = client
			c, err := kubeletContainer(t.Context(), "test", &corev1.Container{Name: "workload"}, status, pod)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %v, want %q", err, tt.wantErr)
				}
				if c != nil {
					t.Error("failed discovery published a partial container")
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			} else {
				wantPath := "/kubepods.slice/kubepods-burstable.slice/kubepods-burstable-podpod_uid.slice/cri-containerd-test.scope"
				if c.InitPid != os.Getpid() ||
					c.CgroupPath != wantPath ||
					c.RuntimeType != "io.containerd.runc.v2" ||
					c.Hostname != "test-pod" ||
					c.LabelHostNamespace() != "test-ns" {
					t.Errorf("unexpected container: %+v", c)
				}
				if len(c.CgroupCss) != 0 || c.NetNamespaceInum != 0 || c.NetNamespaceCookie != 0 {
					t.Error("runtime metadata must not publish unverified resources")
				}
			}
			if client.calls != 1 {
				t.Errorf("CRI calls = %d, want 1", client.calls)
			}
		})
	}
}

func TestPopulateKataContainerUsesHostBinding(t *testing.T) {
	previousProvider := currContainerProvider
	currContainerProvider = containerProviderContainerd
	t.Cleanup(func() { currContainerProvider = previousProvider })
	oldRoot, oldClient := paths.RootfsDefaultPath, containerdClient
	t.Cleanup(func() {
		paths.RootfsDefaultPath, containerdClient = oldRoot, oldClient
		procfs.RootPrefix("")
	})
	paths.RootfsDefaultPath = t.TempDir()
	procRoot := t.TempDir()
	procfs.RootPrefix(procRoot)
	procDir := filepath.Join(procRoot, "proc", "4321")
	if err := os.MkdirAll(procDir, 0o700); err != nil {
		t.Fatal(err)
	}
	membership := "0::/kata/workload\n"
	root := paths.RootfsDefaultPath
	files := []string{"cpu.stat", "cpu.max"}
	if cgroups.CgroupMode() != cgroups.Unified {
		membership = "2:cpu,cpuacct:/kata/workload\n3:memory:/kata/workload\n"
		root = filepath.Join(root, "cpu")
		files = []string{"cpuacct.stat", "cpuacct.usage", "cpu.cfs_quota_us", "cpu.cfs_period_us"}
	}
	if err := os.WriteFile(filepath.Join(procDir, "cgroup"), []byte(membership), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "kata/workload")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c := &podmodel.Container{ID: strings.Repeat("a", 64), RuntimeType: "io.containerd.kata-qemu.v2"}
	if err := populateKataContainer(c, &RuntimeInfo{PID: 4321, SandboxID: "sandbox"}); err != nil {
		t.Fatal(err)
	}
	if c.InitPid != 4321 ||
		c.SandboxID != "sandbox" ||
		c.CgroupPath != "/kata/workload" ||
		len(c.CgroupCss) != 0 ||
		c.NetNamespaceInum != 0 ||
		c.NetNamespaceCookie != 0 {
		t.Errorf("unexpected Host binding: %+v", c)
	}
	// Published Kata members must never fall back to their Guest init.pid.
	published := containerPodForTest(c.ID)
	published.Process = &podmodel.HostProcess{PID: c.InitPid}
	published.Container.RuntimeType, published.Container.SandboxID = c.RuntimeType, c.SandboxID
	published.Container.CgroupPath = c.CgroupPath
	startSourceForTest(t, &sourceFixture{discover: func(context.Context) (podmodel.Discovery, error) {
		return podmodel.Discovery{Pods: map[string]*podmodel.Pod{c.ID: published}, Complete: true}, nil
	}})
	waitContainerForTest(t, c.ID)
	response := runningRuntimeResponse(`{"pid":4321,"runtimeType":"io.containerd.kata-qemu.v2","sandboxID":"sandbox"}`)
	response.Status.Id = c.ID
	containerdClient = &pidRuntimeService{response: response}
	if pid, err := containerInitPIDByID(c.ID); err != nil || pid != 4321 {
		t.Errorf("Kata PID=%d err=%v", pid, err)
	}
	guestPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "orders", Namespace: "payments"},
		Spec:       corev1.PodSpec{NodeName: "worker-96"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	spec := &corev1.Container{
		Name: "collector",
		Env: []corev1.EnvVar{
			{Name: "BAMAI_POD_NAME", Value: "orders"},
			{Name: "BAMAI_POD_NAMESPACE", Value: "payments"},
			{Name: "BAMAI_NODE_NAME", Value: "worker-96"},
		},
	}
	status := &corev1.ContainerStatus{
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(time.Now())}},
	}
	classified, err := kubeletContainer(t.Context(), c.ID, spec, status, guestPod)
	if err != nil || classified == nil || classified.Type != podmodel.ContainerTypeSidecar {
		t.Fatalf("Kata Guest agent classification=%+v err=%v", classified, err)
	}
	spec.Name = "agent"
	spec.Env = nil
	classified, err = kubeletContainer(t.Context(), c.ID, spec, status, guestPod)
	if err != nil || classified == nil || classified.Type != podmodel.ContainerTypeNormal {
		t.Fatalf("business named agent classification=%+v err=%v", classified, err)
	}
	response.Info["info"] = `{"pid":4321,"runtimeType":"io.containerd.kata-qemu.v2","sandboxID":"replaced"}`
	if _, err := containerInitPIDByID(c.ID); err == nil {
		t.Error("changed sandbox must invalidate the binding")
	}
}

// TestRuntimeDiscoveryCancellation 验证容器元数据查询沿用 Manager 的取消信号，
// 不创建脱离本轮发现的后台请求。
func TestRuntimeDiscoveryCancellation(t *testing.T) {
	useContainerdForTest(t)
	previous := containerdClient
	t.Cleanup(func() { containerdClient = previous })
	client := &pidRuntimeService{checkContext: true}
	containerdClient = client
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	record := &podmodel.Container{ID: "checkout-api"}
	if err := populateContainerRuntime(ctx, record, &corev1.Pod{}); !errors.Is(err, context.Canceled) {
		t.Errorf("runtime discovery cancellation=%v", err)
	}
	if client.calls != 1 {
		t.Errorf("runtime calls=%d, want 1", client.calls)
	}
}

func TestCgroupMembershipForSubsystem(t *testing.T) {
	paths := &cgroups.ProcessPaths{
		Controllers: map[string]string{"cpu": "/cpu/container", "memory": "/memory/container"},
		Unified:     "/unified/container",
	}
	tests := []struct {
		name          string
		paths         *cgroups.ProcessPaths
		mode          cgroups.Mode
		subsystem     string
		expectedPath  string
		expectedError string
	}{
		{name: "v1 memory uses its own hierarchy", paths: paths, mode: cgroups.Legacy, subsystem: "memory", expectedPath: "/memory/container"},
		{name: "v1 CPU uses its own hierarchy", paths: paths, mode: cgroups.Legacy, subsystem: "cpu", expectedPath: "/cpu/container"},
		{name: "hybrid memory ignores unified membership", paths: paths, mode: cgroups.Hybrid, subsystem: "memory", expectedPath: "/memory/container"},
		{name: "v2 uses unified membership", paths: paths, mode: cgroups.Unified, subsystem: "memory", expectedPath: "/unified/container"},
		{
			name:  "missing v1 memory does not fall back to CPU or v2",
			paths: &cgroups.ProcessPaths{Controllers: map[string]string{"cpu": "/cpu/container"}, Unified: "/unified/container"},
			mode:  cgroups.Legacy, subsystem: "memory", expectedError: "no memory cgroup v1 membership",
		},
		{
			name:  "missing v2 membership does not fall back to v1",
			paths: &cgroups.ProcessPaths{Controllers: map[string]string{"memory": "/memory/container"}},
			mode:  cgroups.Unified, subsystem: "memory", expectedError: "no cgroup v2 membership",
		},
		{name: "missing paths", mode: cgroups.Legacy, subsystem: "memory", expectedError: "missing process cgroup membership"},
		{name: "unsupported mode", paths: paths, mode: cgroups.Mode(255), subsystem: "memory", expectedError: "unsupported cgroup mode"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actualPath, actualErr := cgroupMembershipForSubsystem(test.paths, test.mode, test.subsystem)
			if test.expectedError != "" {
				if actualErr == nil || !strings.Contains(actualErr.Error(), test.expectedError) {
					t.Fatalf("expected error %q, got path %q and error %v", test.expectedError, actualPath, actualErr)
				}
				return
			}
			if actualErr != nil || actualPath != test.expectedPath {
				t.Fatalf("expected path %q, got %q and error %v", test.expectedPath, actualPath, actualErr)
			}
		})
	}
}
