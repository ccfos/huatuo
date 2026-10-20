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
)

// TestContainerMemoryCgroupPath 验证 Host 兼容路径、Guest 独立路径、统一路径，
// 以及 Guest 缺少 memory 控制器时不会误用 CPU 路径。
func TestContainerMemoryCgroupPath(t *testing.T) {
	for _, tc := range []struct {
		name, memory, want string
		guest              bool
	}{
		{name: "host-default", want: "/cpu/workload"},
		{name: "guest-split", memory: "/memory/workload", want: "/memory/workload", guest: true},
		{name: "guest-unified", memory: "/cpu/workload", want: "/cpu/workload", guest: true},
		{name: "guest-no-memory", guest: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			container := &Container{CgroupPath: "/cpu/workload"}
			if tc.guest {
				container.memoryCgroupPath = &tc.memory
			}
			if got := container.MemoryCgroupPath(); got != tc.want {
				t.Fatalf("memory path=%q, want %q", got, tc.want)
			}
		})
	}
}

// TestFillCssContainers 验证复用的映射会删除旧 CSS，选择最早发现的 Kata 成员，
// 并在代表成员离开后切换，最后清除所有已离开的容器引用。
func TestFillCssContainers(t *testing.T) {
	first := &Container{
		ID: "checkout", RuntimeType: "io.containerd.kata.v2", SandboxID: "checkout-vm",
		Type: ContainerTypeNormal, SyncedAt: time.Unix(10, 0), CgroupCss: map[string]uint64{"cpu": 10},
	}
	second := *first
	second.ID = "checkout-sidecar"
	second.SyncedAt = time.Unix(20, 0)
	members := map[string]*Container{first.ID: first, second.ID: &second}
	destination := map[uint64]*Container{99: first}
	FillCssContainers(members, "cpu", destination)
	if len(destination) != 1 || destination[10] != first {
		t.Fatalf("representatives=%v", destination)
	}
	delete(members, first.ID)
	FillCssContainers(members, "cpu", destination)
	if len(destination) != 1 || destination[10] != &second {
		t.Fatalf("handoff=%v", destination)
	}
	clear(members)
	FillCssContainers(members, "cpu", destination)
	if len(destination) != 0 {
		t.Fatalf("stale containers=%v", destination)
	}
}
