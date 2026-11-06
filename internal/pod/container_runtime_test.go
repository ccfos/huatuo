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
			info, err := readContainerdRuntimeInfo(t.Context(), "test")
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
	if _, err := readContainerdRuntimeInfo(t.Context(), "test"); err == nil {
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
		len(c.CgroupCss) != 0 ||
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
	classified, err := kubeletContainer(t.Context(), c.ID, spec, status, guestPod)
	if err != nil || classified == nil || classified.Type != ContainerTypeSidecar {
		t.Fatalf("Kata Guest agent classification=%+v err=%v", classified, err)
	}
	spec.Name = "agent"
	spec.Env = nil
	classified, err = kubeletContainer(t.Context(), c.ID, spec, status, guestPod)
	if err != nil || classified == nil || classified.Type != ContainerTypeNormal {
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
	record := &Container{ID: "checkout-api"}
	if err := populateContainerRuntime(ctx, record, &corev1.Pod{}); !errors.Is(err, context.Canceled) {
		t.Errorf("runtime discovery cancellation=%v", err)
	}
	if client.calls != 1 {
		t.Errorf("runtime calls=%d, want 1", client.calls)
	}
}

// TestContainerBindingResourceReadiness verifies that CSS and netns availability
// do not gate identity or file metrics, and binding cannot mutate a cached view.
func TestContainerBindingResourceReadiness(t *testing.T) {
	originalRoot := filepath.Dir(procfs.DefaultPath())
	t.Cleanup(func() { procfs.RootPrefix(originalRoot) })
	for _, runtime := range []string{"io.containerd.runc.v2", "io.containerd.kata.v2"} {
		for _, ready := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/ready=%t", runtime, ready), func(t *testing.T) {
				root := t.TempDir()
				procfs.RootPrefix(root)
				procRoot := filepath.Join(root, "proc")
				cgroupRoot := filepath.Join(root, "cgroup")
				guestDirectory(t, cgroupRoot, "/checkout")
				guestFile(t, procRoot, "self/mountinfo", fmt.Sprintf("40 1 0:40 / %s rw - cgroup2 none rw\n", cgroupRoot))
				guestProc(t, &guestScanner{options: GuestScannerOptions{ProcRoot: procRoot}}, 4321, 1, 8500, "0::/checkout\n")
				if ready {
					guestFile(t, procRoot, "4321/ns/net", "")
					var stat syscall.Stat_t
					if err := syscall.Stat(filepath.Join(cgroupRoot, "checkout"), &stat); err != nil {
						t.Error(err)
						return
					}
					for name, css := range map[string]uint64{"cpu": 8500, "memory": 8600} {
						cgroupCssMetaDataMap.Store(css, &containerCssMetaData{CSS: css, Inode: stat.Ino, SubSys: name})
						t.Cleanup(func() { cgroupCssMetaDataMap.Delete(css) })
					}
				}
				original := &Container{
					ID: "checkout", InitPid: 4321, RuntimeType: runtime, SandboxID: "checkout-vm",
					CgroupPath: "/legacy-checkout", IPAddress: "172.16.0.2",
					CgroupCss: map[string]uint64{"cpu": 1}, NetNamespaceInum: 2, NetNamespaceCookie: 3,
				}
				record, err := containerMemoryBinding(original)
				if err != nil {
					t.Error(err)
					return
				}
				if original.CgroupCss["cpu"] != 1 || original.NetNamespaceInum != 2 || original.NetNamespaceCookie != 3 {
					t.Error("binding changed the previously published view")
				}
				if record.pod.Process == nil || record.pod.Process.PID != 4321 || record.startTime != 8500 {
					t.Error("resource readiness lost process identity")
				}
				if path, ok := record.pod.Cgroups.Path("memory"); !ok || path != "/checkout" {
					t.Errorf("memory path=%q available=%t", path, ok)
				}
				view := record.container
				if view.CgroupPath != original.CgroupPath || view.IPAddress != original.IPAddress || view.CgroupCss == nil {
					t.Errorf("legacy path, IP or CSS shape changed: %+v", view)
				}
				if (view.CgroupCss["cpu"] == 8500) != ready {
					t.Errorf("CPU CSS readiness=%v, want %t", view.CgroupCss, ready)
				}
				kata := runtime == "io.containerd.kata.v2"
				if kata && (view.CgroupCss["memory"] != 0 || record.ref.MemoryCgroupPath != "") {
					t.Error("Kata published application memory targets")
				}
				if (view.NetNamespaceInum != 0) != (ready && !kata) || view.NetNamespaceCookie != 0 {
					t.Errorf("network readiness mismatch: %+v", view)
				}
				if kata && record.pod.Network != nil && record.pod.Network.PID != 0 {
					t.Error("Kata VM became a Guest network target")
				}
			})
		}
	}
}
