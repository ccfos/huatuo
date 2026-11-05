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
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
)

func TestCgroupCPUInodeIntegration(t *testing.T) {
	previousDir := bpf.DefaultObjDir
	bpf.DefaultObjDir = integrationTestEnv(t, "HUATUO_BPF_OBJECT_DIR")
	t.Cleanup(func() { bpf.DefaultObjDir = previousDir })
	if err := bpf.Init(nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bpf.Shutdown)
	if err := initCgroupLifecycle(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(releaseCgroupLifecycle)
	paths, err := cgroups.PathsForPID(1)
	if err != nil {
		t.Fatal(err)
	}
	path := paths.Controllers[subsystem.SubsystemCPU]
	if cgroups.CgroupMode() == cgroups.Unified {
		path = paths.Unified
	}
	root := cgroups.RootFsFilePath(subsystem.SubsystemCPU)
	if cgroups.CgroupMode() == cgroups.Unified {
		root = cgroups.RootfsDefaultPath()
	}
	// The one-shot reader joins before we verify the actual inode/CSS mapping.
	if err := triggerContainerCSSSync(root + path); err != nil {
		t.Fatal(err)
	}
	css, err := parseCgroupCPUCSS(path)
	if err != nil || css[subsystem.SubsystemCPU] == 0 {
		t.Fatalf("CPU CSS=%v err=%v", css, err)
	}
}

// TestWorkloadCgroupLifecycleIntegration 创建独立数字名称空 cgroup，验证真实
// CSS 探针、公共绑定和 BPF 删除事件的完整链路。删除资源只使绑定失效，
// 保留 workload 代次和采集状态，不迁移进程或修改现有沙箱。
func TestWorkloadCgroupLifecycleIntegration(t *testing.T) {
	previousDir, previousView := bpf.DefaultObjDir, containerView
	bpf.DefaultObjDir = integrationTestEnv(t, "HUATUO_BPF_OBJECT_DIR")
	store := newTestContainerStore()
	containerView = store
	t.Cleanup(func() { bpf.DefaultObjDir, containerView = previousDir, previousView })
	if err := bpf.Init(nil); err != nil {
		t.Errorf("initialize BPF: %v", err)
		return
	}
	t.Cleanup(bpf.Shutdown)
	if err := initCgroupLifecycle(); err != nil {
		t.Errorf("initialize cgroup probes: %v", err)
		return
	}
	t.Cleanup(releaseCgroupLifecycle)
	membership := "/" + strconv.FormatInt(time.Now().UnixNano(), 10)
	root := cgroups.RootFsFilePath(subsystem.SubsystemCPU)
	locations := CgroupBinding{Controllers: map[string]string{"cpu": membership}}
	if cgroups.CgroupMode() == cgroups.Unified {
		root = cgroups.RootfsDefaultPath()
		locations = CgroupBinding{Unified: membership}
	}
	directory := filepath.Join(root, membership)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Errorf("create isolated cgroup: %v", err)
		return
	}
	t.Cleanup(func() {
		if err := os.Remove(directory); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("remove isolated cgroup: %v", err)
		}
	})
	if _, err := os.Stat(filepath.Join(directory, "cpu.stat")); err != nil {
		t.Errorf("isolated cgroup has no CPU controller: %v", err)
		return
	}
	mounts, err := cgroupMounts()
	if err != nil {
		t.Errorf("read cgroup mounts: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var binding CgroupBinding
	for {
		binding = bindCgroups(locations, mounts)
		if binding.CSS["cpu"] != 0 && binding.kernelGroups["cpu"] != 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Errorf("kernel CSS/group association did not become available: %v", ctx.Err())
			return
		case <-ticker.C:
		}
	}
	record := &containerRecord{pod: &Pod{ID: "checkout-sandbox", Type: TypeCubeSandbox, Cgroups: binding}}
	store.commit(containerSnapshot{records: map[string]*containerRecord{record.pod.ID: record}, isComplete: true}, nil)
	baseline := &struct{ value uint64 }{value: 6400}
	record.pod.lifeResources["kernel-lifecycle"] = baseline
	subscription, err := store.subscribePods(t.Context())
	if err != nil {
		t.Errorf("subscribe workload lifecycle: %v", err)
		return
	}
	t.Cleanup(subscription.Close)
	_, _ = subscription.DrainEvents()
	if err := os.Remove(directory); err != nil {
		t.Errorf("remove bound cgroup: %v", err)
		return
	}
	select {
	case <-subscription.Notify():
	case <-ctx.Done():
		t.Errorf("BPF deletion did not invalidate the numeric cgroup: %v", ctx.Err())
		return
	}
	update, err := subscription.DrainEvents()
	if err != nil || update.Mode != PodUpdateFull || len(update.Events) != 1 {
		t.Errorf("kernel resource invalidation=%+v, err=%v", update, err)
		return
	}
	event := &update.Events[0]
	if event.Key != record.ref.Key || len(event.Pod.Cgroups.CSS) != 0 || event.Pod.LifeResources("kernel-lifecycle") != baseline {
		t.Errorf("kernel deletion changed membership or retained CSS: %+v", event)
	}
	if _, err := event.Pod.Cgroups.Directory("cpu"); err == nil {
		t.Error("kernel-deleted cgroup remains bound")
	}
}
