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
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
)

func TestHostKataSandboxID(t *testing.T) {
	for _, tt := range []struct {
		name, runtime, sandbox, want string
	}{
		{"host kata", "io.containerd.kata-qemu.v2", "vm", "vm"},
		{"missing sandbox", "io.containerd.kata.v2", "", ""},
		{"runc pod sandbox", "io.containerd.runc.v2", "pod", ""},
		{"guest", "", "pod", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := &Container{RuntimeType: tt.runtime, SandboxID: tt.sandbox}
			if got := c.HostKataSandboxID(); got != tt.want {
				t.Errorf("HostKataSandboxID = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsKataRuntime(t *testing.T) {
	for _, tt := range []struct {
		runtime string
		want    bool
	}{
		{"io.containerd.kata.v2", true},
		{"io.containerd.kata-qemu.v2", true},
		{"io.containerd.kata-fc.v2", true},
		{"io.containerd.runc.v2", false},
		{"io.containerd.katalog.v2", false},
		{"io.containerd.kata-qemu.v1", false},
		{"", false},
	} {
		t.Run(tt.runtime, func(t *testing.T) {
			if got := IsKataRuntime(tt.runtime); got != tt.want {
				t.Errorf("got %t, want %t", got, tt.want)
			}
		})
	}
}

func TestKataCSSRepresentative(t *testing.T) {
	oldView := containerView
	t.Cleanup(func() { containerView = oldView })
	first := &Container{
		ID:          "z-first",
		RuntimeType: "io.containerd.kata-qemu.v2",
		SandboxID:   "vm",
		Type:        ContainerTypeNormal,
		SyncedAt:    time.Unix(1, 0),
		CgroupCss:   map[string]uint64{subsystem.SubsystemCPU: 10},
	}
	second := &Container{
		ID:          "a-second",
		RuntimeType: first.RuntimeType,
		SandboxID:   "vm",
		Type:        ContainerTypeNormal,
		SyncedAt:    time.Unix(2, 0),
		CgroupCss:   map[string]uint64{subsystem.SubsystemCPU: 10},
	}
	runc := &Container{
		ID:          "runc",
		RuntimeType: "io.containerd.runc.v2",
		Type:        ContainerTypeNormal,
		CgroupCss:   map[string]uint64{subsystem.SubsystemCPU: 20},
	}
	members := map[string]*Container{first.ID: first, second.ID: second, runc.ID: runc}
	check := func(want *Container) {
		t.Helper()
		containerView = newTestContainerStore()
		for id, member := range members {
			containerView.records[id] = &containerRecord{container: member}
		}
		for range 100 {
			byCSS := BuildCssContainers(members, subsystem.SubsystemCPU)
			if byCSS[10] != want || byCSS[20] != runc || len(byCSS) != 2 {
				t.Errorf("CSS mapping = %v, want VM representative %s and runc", byCSS, want.ID)
				return
			}
			ids, err := GetCSSToContainerID(subsystem.SubsystemCPU)
			if err != nil || ids[10] != want.ID || ids[20] != runc.ID {
				t.Errorf("CSS IDs = %v, error %v", ids, err)
				return
			}
			got, err := ContainerByCSS(10, subsystem.SubsystemCPU)
			if err != nil || got != want {
				t.Errorf("ContainerByCSS = %v, error %v, want %s", got, err, want.ID)
				return
			}
		}
	}
	check(first)
	// A new member with an earlier lexical ID cannot steal attribution.
	third := *second
	third.ID, third.SyncedAt = "0-new", time.Unix(3, 0)
	members[third.ID] = &third
	check(first)
	delete(members, first.ID)
	check(second)
	// Equal discovery times still produce a deterministic representative.
	third.SyncedAt = second.SyncedAt
	check(&third)
}

func TestKataRefreshPreservesFirstDiscovery(t *testing.T) {
	store := newTestContainerStore()
	first := containerRecordForTest("first")
	first.container.RuntimeType = "io.containerd.kata-qemu.v2"
	first.container.SandboxID = "vm"
	first.container.SyncedAt = time.Unix(1, 0)
	store.commit(containerSnapshot{records: map[string]*containerRecord{"first": first}, isComplete: true}, nil)
	refreshed := containerRecordForTest("first")
	refreshed.container.RuntimeType = first.container.RuntimeType
	refreshed.container.SandboxID = first.container.SandboxID
	refreshed.container.SyncedAt = time.Unix(10, 0)
	store.commit(containerSnapshot{records: map[string]*containerRecord{"first": refreshed}, isComplete: true}, nil)
	if !refreshed.container.SyncedAt.Equal(first.container.SyncedAt) {
		t.Errorf("refresh changed discovery time: %s", refreshed.container.SyncedAt)
	}
}

func TestKataRepresentativePrefersBusinessOverGuestAgent(t *testing.T) {
	agent := &Container{
		ID:          "agent-first",
		Type:        ContainerTypeSidecar,
		RuntimeType: "io.containerd.kata-qemu.v2",
		SandboxID:   "orders-vm",
		SyncedAt:    time.Unix(1, 0),
		CgroupCss:   map[string]uint64{"cpu": 10},
	}
	business := *agent
	business.ID = "checkout"
	business.Type = ContainerTypeNormal
	business.SyncedAt = time.Unix(2, 0)
	members := map[string]*Container{agent.ID: agent, business.ID: &business}
	for range 100 {
		if got := BuildCssContainers(members, "cpu")[10]; got != &business {
			t.Errorf("representative=%v, want checkout", got)
			break
		}
		if got := BuildCssContainersID(members, "cpu")[10]; got != business.ID {
			t.Errorf("representative ID=%q, want checkout", got)
			break
		}
	}
}

// TestPodsByCgroupCSS 验证公共查询只返回非零控制器绑定，容器筛选不作用于沙箱；
// Kata 代表沿用首次发现时间，刷新 Pod.SyncedAt 不换代表，删除后才切换。
func TestPodsByCgroupCSS(t *testing.T) {
	previous := containerView
	containerView = newTestContainerStore()
	t.Cleanup(func() { containerView = previous })
	members := map[string]*Container{
		"checkout-first":   {ID: "checkout-first", Type: ContainerTypeNormal, RuntimeType: "io.containerd.kata.v2", SandboxID: "checkout-vm", SyncedAt: time.Unix(100, 0), CgroupCss: map[string]uint64{"cpu": 8100}},
		"checkout-next":    {ID: "checkout-next", Type: ContainerTypeNormal, RuntimeType: "io.containerd.kata.v2", SandboxID: "checkout-vm", SyncedAt: time.Unix(200, 0), CgroupCss: map[string]uint64{"cpu": 8100}},
		"checkout-sidecar": {ID: "checkout-sidecar", Type: ContainerTypeSidecar, CgroupCss: map[string]uint64{"cpu": 8200}},
		"checkout-unbound": {ID: "checkout-unbound", Type: ContainerTypeNormal},
	}
	for id, container := range members {
		container.Qos = ContainerQosLevelMin
		containerView.records[id] = &containerRecord{pod: podFromContainer(container), container: container}
	}
	sandbox := &Pod{ID: "checkout-sandbox", Type: TypeCubeSandbox, Cgroups: CgroupBinding{CSS: map[string]uint64{"cpu": 8300, "memory": 8400}}}
	containerView.records[sandbox.ID] = &containerRecord{pod: sandbox}
	for range 20 {
		owners := PodsByCgroupCSS("cpu", ContainerTypeNormal)
		if len(owners) != 2 || owners[8100] != containerView.records["checkout-first"].pod || owners[8300] != sandbox {
			t.Errorf("CPU owners=%v", owners)
		}
	}
	containerView.records["checkout-first"].pod.SyncedAt = time.Unix(500, 0)
	if got := PodsByCgroupCSS("cpu", ContainerTypeNormal)[8100]; got == nil || got.ID != "checkout-first" {
		t.Errorf("refresh changed Kata representative: %v", got)
	}
	delete(containerView.records, "checkout-first")
	if got := PodsByCgroupCSS("cpu", ContainerTypeNormal)[8100]; got == nil || got.ID != "checkout-next" {
		t.Errorf("deleted representative was not replaced: %v", got)
	}
	if got := PodsByCgroupCSS("cpu", ContainerTypeNormal|ContainerTypeSidecar); len(got) != 3 {
		t.Errorf("sidecar selection=%v", got)
	}
	if got := PodsByCgroupCSS("memory", ContainerTypeNormal); len(got) != 1 || got[8400] != sandbox {
		t.Errorf("memory owners=%v", got)
	}
	if got := PodsByCgroupCSS("blkio", ContainerTypeNormal); len(got) != 0 {
		t.Errorf("unbound controller owners=%v", got)
	}
}
