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
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
)

func TestContainerIDByCgroupNetNamespace(t *testing.T) {
	previous := containerView
	containerView = newContainerStore()
	containerView.isActive, containerView.err = true, nil
	fixtures := map[string]*Container{
		"css": {
			ID:        "css",
			Type:      ContainerTypeNormal,
			CgroupCss: map[string]uint64{subsystem.SubsystemMemory: 11},
		},
		"cookie": {
			ID:                 "cookie",
			Type:               ContainerTypeNormal,
			NetNamespaceCookie: 22,
		},
		"inum": {
			ID:               "inum",
			Type:             ContainerTypeNormal,
			NetNamespaceInum: 33,
		},
	}
	for id, container := range fixtures {
		containerView.records[id] = &containerRecord{container: container}
	}
	t.Cleanup(func() { containerView = previous })

	tests := []struct {
		name string
		ids  ContainerCgroupNetNamespace
		want string
	}{
		{
			name: "css takes precedence",
			ids: ContainerCgroupNetNamespace{
				MemoryCgroupCSSAddr: 11,
				NetNamespaceCookie:  22,
				NetNamespaceInum:    33,
			},
			want: "css",
		},
		{
			name: "net namespace cookie falls back after CSS miss",
			ids: ContainerCgroupNetNamespace{
				MemoryCgroupCSSAddr: 99,
				NetNamespaceCookie:  22,
				NetNamespaceInum:    33,
			},
			want: "cookie",
		},
		{
			name: "net namespace inum falls back after CSS and cookie misses",
			ids: ContainerCgroupNetNamespace{
				MemoryCgroupCSSAddr: 99,
				NetNamespaceCookie:  88,
				NetNamespaceInum:    33,
			},
			want: "inum",
		},
		{
			name: "no matching metadata",
			ids: ContainerCgroupNetNamespace{
				MemoryCgroupCSSAddr: 99,
				NetNamespaceCookie:  88,
				NetNamespaceInum:    77,
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ContainerIDByCgroupNetNamespace(tt.ids)
			if got != tt.want {
				t.Errorf("ContainerIDByCgroupNetNamespace(%+v) = %q, want %q", tt.ids, got, tt.want)
			}
		})
	}
}

// TestWorkloadHostResourceBinding 验证进程和网络独立绑定，覆盖只有进程、
// 只有 netns、只有 IP、进程不存在、命名空间不符以及代表进程和网络 PID 不同。
// 缺少进程或网络不影响已有 cgroup；未经过绑定的 PID 不可直接用于采集。
func TestWorkloadHostResourceBinding(t *testing.T) {
	scanner := guestFixture(t)
	for _, pid := range []int{150, 250} {
		guestProc(t, scanner, pid, 1, uint64(pid), "0::/checkout\n")
	}
	guestFile(t, scanner.options.ProcRoot, "250/ns/net", "")
	namespace, err := os.Stat(filepath.Join(scanner.options.ProcRoot, "250/ns/net"))
	if err != nil {
		t.Errorf("read fixture namespace: %v", err)
		return
	}
	inode := namespace.Sys().(*syscall.Stat_t).Ino
	cases := []struct {
		name        string
		process     *HostProcess
		network     *HostNetwork
		wantProcess int
		wantNetwork int
	}{
		{name: "process only", process: &HostProcess{PID: 150}, wantProcess: 150},
		{name: "network only", network: &HostNetwork{PID: 250}, wantNetwork: 250},
		{name: "IP metadata only", network: &HostNetwork{IPAddress: "172.16.0.2"}},
		{name: "nonexistent process", process: &HostProcess{PID: 350}, network: &HostNetwork{PID: 350, IPAddress: "172.16.0.2"}},
		{name: "namespace mismatch", process: &HostProcess{PID: 150}, network: &HostNetwork{PID: 250, NamespaceInum: inode + 1}, wantProcess: 150},
		{name: "independent process and network", process: &HostProcess{PID: 150}, network: &HostNetwork{PID: 250, NamespaceInum: inode, NamespaceCookie: ^uint64(0)}, wantProcess: 150, wantNetwork: 250},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			workload := &Pod{
				ID: "checkout-workload", Process: tc.process, Network: tc.network,
				Cgroups: CgroupBinding{Unified: "/checkout", directories: map[string]string{"": scanner.options.CgroupRoot}},
			}
			if _, err := workload.ProcessPID(); err == nil {
				t.Error("unbound source PID accepted for collection")
			}
			if _, err := workload.NetworkPID(); err == nil {
				t.Error("unbound source namespace accepted for collection")
			}
			bindHostResources(workload, scanner.options.ProcRoot)
			process, processErr := workload.ProcessPID()
			if process != tc.wantProcess || (processErr == nil) != (tc.wantProcess != 0) {
				t.Errorf("process PID=%d, err=%v, want %d", process, processErr, tc.wantProcess)
			}
			network, networkErr := workload.NetworkPID()
			if network != tc.wantNetwork || (networkErr == nil) != (tc.wantNetwork != 0) {
				t.Errorf("network PID=%d, err=%v, want %d", network, networkErr, tc.wantNetwork)
			}
			if workload.Network != nil && workload.Network.NamespaceCookie != 0 {
				t.Error("source cookie published without local verification")
			}
			if tc.network != nil && tc.network.IPAddress != "" &&
				(workload.Network == nil || workload.Network.IPAddress != tc.network.IPAddress) {
				t.Error("unavailable namespace discarded IP metadata")
			}
			if _, err := workload.Cgroups.Directory("cpu"); err != nil {
				t.Errorf("independent cgroup became unavailable: %v", err)
			}
		})
	}
}

// TestWorkloadHostResourceInvalidation 验证 PID 启动时间变化、netns 变化和进程
// 消失后旧绑定不能继续采集；单独的 netns 变化不使仍有效的代表进程失效。
func TestWorkloadHostResourceInvalidation(t *testing.T) {
	for _, change := range []string{"process start time", "network namespace", "process exit"} {
		t.Run(change, func(t *testing.T) {
			scanner := guestFixture(t)
			guestProc(t, scanner, 150, 1, 150, "0::/checkout\n")
			guestFile(t, scanner.options.ProcRoot, "150/ns/net", "")
			workload := &Pod{ID: "checkout-workload", Process: &HostProcess{PID: 150}, Network: &HostNetwork{PID: 150}}
			bindHostResources(workload, scanner.options.ProcRoot)
			if _, err := workload.NetworkPID(); err != nil {
				t.Errorf("initial network binding: %v", err)
				return
			}
			switch change {
			case "process start time":
				guestProc(t, scanner, 150, 1, 250, "0::/checkout\n")
			case "network namespace":
				path := filepath.Join(scanner.options.ProcRoot, "150/ns/net")
				if err := os.Rename(path, path+".previous"); err != nil {
					t.Errorf("preserve previous namespace inode: %v", err)
					return
				}
				guestFile(t, scanner.options.ProcRoot, "150/ns/net", "")
			case "process exit":
				if err := os.RemoveAll(filepath.Join(scanner.options.ProcRoot, "150")); err != nil {
					t.Errorf("remove process fixture: %v", err)
					return
				}
			}
			if _, err := workload.NetworkPID(); err == nil {
				t.Error("invalidated namespace remains available")
			}
			_, err := workload.ProcessPID()
			if (err == nil) != (change == "network namespace") {
				t.Errorf("representative process after %s: %v", change, err)
			}
		})
	}
}

func TestBuildCSSMapsExcludeInvalidAddresses(t *testing.T) {
	valid := &Container{
		ID:        "valid",
		CgroupCss: map[string]uint64{subsystem.SubsystemMemory: 42, subsystem.SubsystemCPU: 84},
	}
	containers := map[string]*Container{
		"valid":   valid,
		"zero":    {CgroupCss: map[string]uint64{subsystem.SubsystemMemory: 0, subsystem.SubsystemCPU: 0}},
		"missing": {},
	}
	for subsys, address := range map[string]uint64{subsystem.SubsystemMemory: 42, subsystem.SubsystemCPU: 84} {
		t.Run(subsys, func(t *testing.T) {
			pointers := BuildCssContainers(containers, subsys)
			ids := BuildCssContainersID(containers, subsys)
			if len(pointers) != 1 || pointers[address] != valid || len(ids) != 1 || ids[address] != "valid" {
				t.Fatalf("invalid CSS mapping: containers=%v ids=%v", pointers, ids)
			}
		})
	}
}
