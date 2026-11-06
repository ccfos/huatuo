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
	"os"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/cgroups"
	podmodel "github.com/ccfos/huatuo/internal/pod"
)

func TestKataRuntimeIntegration(t *testing.T) {
	id := os.Getenv("HUATUO_KATA_CONTAINER_ID")
	if id == "" {
		t.Skip("requires HUATUO_KATA_CONTAINER_ID")
	}
	previous := bpf.DefaultObjDir
	bpf.DefaultObjDir = os.Getenv("HUATUO_BPF_OBJECT_DIR")
	t.Cleanup(func() { bpf.DefaultObjDir = previous })
	if err := bpf.Init(nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bpf.Shutdown)
	if err := initContainerProviderEnv(t.Context(), containerProviderContainerd, ""); err != nil {
		t.Fatal(err)
	}
	ids := []string{id}
	if peer := os.Getenv("HUATUO_KATA_PEER_CONTAINER_ID"); peer != "" {
		ids = append(ids, peer)
	}
	pods := make(map[string]*podmodel.Pod, len(ids))
	for _, id := range ids {
		info, err := readContainerdRuntimeInfo(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if !podmodel.IsKataRuntime(info.RuntimeType) {
			t.Fatalf("runtime %q is not Kata", info.RuntimeType)
		}
		container := &podmodel.Container{ID: id, RuntimeType: info.RuntimeType}
		if err := populateKataContainer(container, info); err != nil {
			t.Fatal(err)
		}
		paths, err := cgroups.PathsForPID(container.InitPid)
		if err != nil {
			t.Fatal(err)
		}
		workload := containerPodForTest(id)
		workload.Cgroups = podmodel.CgroupBinding{Unified: paths.Unified, Controllers: paths.Controllers}
		workload.Process = &podmodel.HostProcess{PID: container.InitPid}
		workload.Container.RuntimeType, workload.Container.SandboxID = container.RuntimeType, container.SandboxID
		workload.Container.CgroupPath = container.CgroupPath
		pods[id] = workload
	}
	startSourceForTest(t, &sourceFixture{discover: func(context.Context) (podmodel.Discovery, error) {
		return podmodel.Discovery{Pods: pods, Complete: true}, nil
	}})
	first := *waitContainerForTest(t, id)
	if first.CgroupCss["cpu"] == 0 || first.SandboxID == "" || first.InitPid <= 0 {
		t.Fatalf("incomplete binding: %+v", first)
	}
	if first.NetNamespaceInum != 0 || first.NetNamespaceCookie != 0 {
		t.Error("Host namespace identified Guest network")
	}
	if len(ids) > 1 {
		peer := *waitContainerForTest(t, ids[1])
		if peer.InitPid != first.InitPid || peer.SandboxID != first.SandboxID || peer.CgroupPath != first.CgroupPath || peer.CgroupCss["cpu"] != first.CgroupCss["cpu"] {
			t.Fatal("shared VM binding differs")
		}
		first.SyncedAt, peer.SyncedAt = time.Unix(1, 0), time.Unix(2, 0)
		members := map[string]*podmodel.Container{first.ID: &first, peer.ID: &peer}
		for range 100 {
			if podmodel.BuildCssContainers(members, "cpu")[first.CgroupCss["cpu"]] != &first {
				t.Fatal("unstable representative")
			}
		}
		delete(members, first.ID)
		if podmodel.BuildCssContainers(members, "cpu")[peer.CgroupCss["cpu"]] != &peer {
			t.Fatal("remaining peer was not selected")
		}
	}
	t.Logf("runtime=%s pid=%d sandbox=%s path=%s cpu_css=%#x", first.RuntimeType, first.InitPid, first.SandboxID, first.CgroupPath, first.CgroupCss["cpu"])
}
