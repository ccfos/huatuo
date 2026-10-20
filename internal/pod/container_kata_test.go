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
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/cgroups/subsystem"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/paths"
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
			if got := isKataRuntime(tt.runtime); got != tt.want {
				t.Errorf("got %t, want %t", got, tt.want)
			}
		})
	}
}

func TestKataCgroupPath(t *testing.T) {
	old := paths.RootfsDefaultPath
	t.Cleanup(func() { paths.RootfsDefaultPath = old })
	for _, tt := range []struct {
		name, proc, want string
		mode             cgroups.Mode
		missingFile      bool
		wantErr          bool
	}{
		{
			name: "literal colon path",
			proc: "0::/pod.slice:cri-containerd:sandbox\n",
			want: "/pod.slice:cri-containerd:sandbox",
			mode: cgroups.Unified,
		},
		{
			name: "systemd nested path",
			proc: "0::/kubepods.slice/pod.slice/cri-containerd-sandbox.scope\n",
			want: "/kubepods.slice/pod.slice/cri-containerd-sandbox.scope",
			mode: cgroups.Unified,
		},
		{
			name: "cgroup v1",
			proc: "2:cpu,cpuacct:/kata/sandbox\n3:memory:/kata/sandbox\n",
			want: "/kata/sandbox",
			mode: cgroups.Legacy,
		},
		{
			name: "hybrid uses v1 controllers",
			proc: "0::/unified\n2:cpu,cpuacct:/kata/sandbox\n3:memory:/kata/sandbox\n",
			want: "/kata/sandbox",
			mode: cgroups.Hybrid,
		},
		{
			name:        "missing stat file",
			proc:        "0::/kata/sandbox\n",
			want:        "/kata/sandbox",
			mode:        cgroups.Unified,
			missingFile: true,
			wantErr:     true,
		},
		{name: "empty path", proc: "0::\n", mode: cgroups.Unified, wantErr: true},
		{name: "root cgroup", proc: "0::/\n", mode: cgroups.Unified, wantErr: true},
		{name: "relative path", proc: "0::kata/sandbox\n", mode: cgroups.Unified, wantErr: true},
		{name: "namespace relative path", proc: "0::/../sandbox\n", mode: cgroups.Unified, wantErr: true},
		{name: "malformed record", proc: "invalid\n", mode: cgroups.Unified, wantErr: true},
		{name: "v1 cannot stand in for v2", proc: "2:cpu,cpuacct:/kata/sandbox\n", mode: cgroups.Unified, wantErr: true},
		{
			name:    "different controller paths",
			proc:    "2:cpu,cpuacct:/kata/sandbox\n3:memory:/another\n",
			mode:    cgroups.Legacy,
			wantErr: true,
		},
		{name: "unsupported mode", proc: "0::/kata/sandbox\n", mode: cgroups.Unavailable, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			paths.RootfsDefaultPath = t.TempDir()
			procFile := filepath.Join(t.TempDir(), "cgroup")
			if err := os.WriteFile(procFile, []byte(tt.proc), 0o600); err != nil {
				t.Fatal(err)
			}
			if tt.want != "" {
				root := paths.RootfsDefaultPath
				files := []string{"cpu.stat", "cpu.max"}
				if tt.mode == cgroups.Legacy || tt.mode == cgroups.Hybrid {
					root = filepath.Join(root, "cpu")
					files = []string{"cpuacct.stat", "cpuacct.usage", "cpu.cfs_quota_us", "cpu.cfs_period_us"}
				}
				dir := filepath.Join(root, tt.want)
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				for i, name := range files {
					if tt.missingFile && i == 0 {
						continue
					}
					if err := os.WriteFile(filepath.Join(dir, name), []byte("0\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			got, err := kataCgroupPath(procFile, tt.mode)
			if (err != nil) != tt.wantErr {
				t.Errorf("error = %v, want error %t", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("path = %q, want %q", got, tt.want)
			}
		})
	}
	if _, err := kataCgroupPath(filepath.Join(t.TempDir(), "missing"), cgroups.Unified); err == nil {
		t.Error("missing process must fail")
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
