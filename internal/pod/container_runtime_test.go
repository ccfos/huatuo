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
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/paths"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	"github.com/ccfos/huatuo/internal/procfs"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	criapi "k8s.io/cri-api/pkg/apis"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

type pidRuntimeService struct {
	criapi.RuntimeService
	response    *runtimeapi.ContainerStatusResponse
	err         error
	calls       int
	containerID string
	verbose     bool
}

func (s *pidRuntimeService) ContainerStatus(
	_ context.Context,
	id string,
	verbose bool,
) (*runtimeapi.ContainerStatusResponse, error) {
	s.calls++
	s.containerID = id
	s.verbose = verbose
	return s.response, s.err
}

func runningRuntimeResponse(info string) *runtimeapi.ContainerStatusResponse {
	return &runtimeapi.ContainerStatusResponse{
		Status: &runtimeapi.ContainerStatus{Id: "test", State: runtimeapi.ContainerState_CONTAINER_RUNNING},
		Info:   map[string]string{"info": info},
	}
}

func TestReadContainerdRuntimeInfo(t *testing.T) {
	old := containerdClient
	t.Cleanup(func() { containerdClient = old })
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
			containerdClient = client
			info, err := readContainerdRuntimeInfo("test")
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
	containerdClient = nil
	if _, err := readContainerdRuntimeInfo("test"); err == nil {
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
			c, err := kubeletContainer("test", &corev1.Container{Name: "workload"}, status, pod)
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
	oldRoot, oldClient, oldNames := paths.RootfsDefaultPath, containerdClient, cgroupCssID2SubSysNameMap
	t.Cleanup(func() {
		paths.RootfsDefaultPath, containerdClient, cgroupCssID2SubSysNameMap = oldRoot, oldClient, oldNames
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
	var st syscall.Stat_t
	if err := syscall.Stat(dir, &st); err != nil {
		t.Fatal(err)
	}
	cgroupCssID2SubSysNameMap = map[int]string{0: subsystem.SubsystemCPU}
	e := containerCssPerfEvent{Inode: st.Ino}
	e.CSS[0] = 987654
	if err := cgroupUpdateOrCreateCssData(&e, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cgroupCssMetaDataMap.Delete(e.CSS[0]) })
	c := &Container{ID: strings.Repeat("a", 64), RuntimeType: "io.containerd.kata-qemu.v2"}
	if err := populateKataContainer(c, &containerdRuntimeInfo{PID: 4321, SandboxID: "sandbox"}); err != nil {
		t.Fatal(err)
	}
	if c.InitPid != 4321 ||
		c.SandboxID != "sandbox" ||
		c.CgroupPath != "/kata/workload" ||
		c.CgroupCss[subsystem.SubsystemCPU] != e.CSS[0] ||
		c.NetNamespaceInum != 0 ||
		c.NetNamespaceCookie != 0 {
		t.Errorf("unexpected Host binding: %+v", c)
	}
	// Published Kata members must never fall back to their Guest init.pid.
	oldView := containerView
	t.Cleanup(func() { containerView = oldView })
	containerView = newTestContainerStore()
	containerView.records[c.ID] = &containerRecord{container: c}
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
	classified, err := kubeletContainer(c.ID, spec, status, guestPod)
	if err != nil || classified == nil || classified.Type != ContainerTypeSidecar {
		t.Fatalf("Kata Guest agent classification=%+v err=%v", classified, err)
	}
	spec.Name = "agent"
	spec.Env = nil
	classified, err = kubeletContainer(c.ID, spec, status, guestPod)
	if err != nil || classified == nil || classified.Type != ContainerTypeNormal {
		t.Fatalf("business named agent classification=%+v err=%v", classified, err)
	}
	response.Info["info"] = `{"pid":4321,"runtimeType":"io.containerd.kata-qemu.v2","sandboxID":"replaced"}`
	if _, err := containerInitPIDByID(c.ID); err == nil {
		t.Error("changed sandbox must invalidate the binding")
	}
}
