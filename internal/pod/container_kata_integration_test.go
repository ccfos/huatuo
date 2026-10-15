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
	"os"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
)

// Only inspect an explicitly selected, already running container.
func TestKataRuntimeIntegration(t *testing.T) {
	id := integrationTestEnv(t, "HUATUO_KATA_CONTAINER_ID")
	previousDir := bpf.DefaultObjDir
	bpf.DefaultObjDir = integrationTestEnv(t, "HUATUO_BPF_OBJECT_DIR")
	t.Cleanup(func() { bpf.DefaultObjDir = previousDir })
	if err := bpf.Init(nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bpf.Shutdown)
	if err := initContainerProviderEnv(containerProviderContainerd, ""); err != nil {
		t.Fatal(err)
	}
	if err := containerCgroupCssInit(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(containerCgroupCssRelease)
	info, err := readContainerdRuntimeInfo(id)
	if err != nil {
		t.Fatal(err)
	}
	if !isKataRuntime(info.RuntimeType) {
		t.Fatalf("selected runtime %q is not Kata", info.RuntimeType)
	}
	c := &Container{ID: id, RuntimeType: info.RuntimeType}
	if err := populateKataContainer(c, info); err != nil {
		t.Fatal(err)
	}
	if c.CgroupCss[subsystem.SubsystemCPU] == 0 || c.SandboxID == "" || c.InitPid <= 0 {
		t.Fatalf("incomplete binding: %+v", c)
	}
	if _, err := containerMemoryBinding(c); err != nil {
		t.Fatal(err)
	}
	if c.NetNamespaceInum != 0 || c.NetNamespaceCookie != 0 {
		t.Fatal("Host VM namespace must not identify a Guest network namespace")
	}
	if peerID := os.Getenv("HUATUO_KATA_PEER_CONTAINER_ID"); peerID != "" {
		peerInfo, err := readContainerdRuntimeInfo(peerID)
		if err != nil {
			t.Fatal(err)
		}
		peer := &Container{ID: peerID, RuntimeType: peerInfo.RuntimeType}
		if err := populateKataContainer(peer, peerInfo); err != nil {
			t.Fatal(err)
		}
		if peer.InitPid != c.InitPid || peer.SandboxID != c.SandboxID || peer.CgroupPath != c.CgroupPath || peer.CgroupCss[subsystem.SubsystemCPU] != c.CgroupCss[subsystem.SubsystemCPU] {
			t.Fatalf("shared VM has different Host binding: first=%+v peer=%+v", c, peer)
		}
		c.SyncedAt = time.Unix(1, 0)
		peer.SyncedAt = time.Unix(2, 0)
		members := map[string]*Container{c.ID: c, peer.ID: peer}
		css := c.CgroupCss[subsystem.SubsystemCPU]
		for range 100 {
			if BuildCssContainers(members, subsystem.SubsystemCPU)[css] != c {
				t.Fatal("shared VM representative is unstable")
			}
		}
		delete(members, c.ID)
		if BuildCssContainers(members, subsystem.SubsystemCPU)[css] != peer {
			t.Fatal("remaining member did not become representative")
		}
		t.Logf("shared VM peer=%s matched the same PID, sandbox, path and CPU CSS", peer.ID)
	}

	t.Logf("runtime=%s pid=%d sandbox=%s path=%s cpu_css=%#x", c.RuntimeType, c.InitPid, c.SandboxID, c.CgroupPath, c.CgroupCss[subsystem.SubsystemCPU])
}
