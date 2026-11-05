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
	"strings"
	"syscall"
	"testing"

	promprocfs "github.com/prometheus/procfs"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/paths"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
)

func TestCgroupCPUCSS(t *testing.T) {
	oldRoot, oldNames := paths.RootfsDefaultPath, cgroupCssID2SubSysNameMap
	paths.RootfsDefaultPath = t.TempDir()
	cgroupCssID2SubSysNameMap = map[int]string{0: subsystem.SubsystemCPU, 1: subsystem.SubsystemMemory}
	t.Cleanup(func() {
		paths.RootfsDefaultPath, cgroupCssID2SubSysNameMap = oldRoot, oldNames
	})
	root := cgroups.RootFsFilePath(subsystem.SubsystemCPU)
	if cgroups.CgroupMode() == cgroups.Unified {
		root = cgroups.RootfsDefaultPath()
	}
	sandboxID := strings.Repeat("a", 64)
	vmPath := "/kubepods-burstable-pod" + strings.Repeat("b", 36) + ".slice:cri-containerd:" + sandboxID
	overheadPath := "/kata_overhead/" + sandboxID
	for i, path := range []string{vmPath, overheadPath} {
		dir := filepath.Join(root, path)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		var st syscall.Stat_t
		if err := syscall.Stat(dir, &st); err != nil {
			t.Fatal(err)
		}
		event := containerCssPerfEvent{Inode: st.Ino}
		event.CSS[0] = uint64(100 + i)
		event.CSS[1] = uint64(200 + i)
		// Match bpf_probe_read_str: leave a terminating NUL even for long names.
		copy(event.KnodeName[:len(event.KnodeName)-1], filepath.Base(path))
		if err := cgroupUpdateOrCreateCssData(&event, extractContainerID(string(event.KnodeName[:]))); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cgroupCssMetaDataMap.Delete(event.CSS[0])
			cgroupCssMetaDataMap.Delete(event.CSS[1])
		})
	}

	t.Run("long VM name is independent of container ID and overhead", func(t *testing.T) {
		got, err := parseCgroupCPUCSS(vmPath)
		if err != nil || len(got) != 1 || got[subsystem.SubsystemCPU] != 100 {
			t.Errorf("VM CPU CSS = %v, err = %v; want cpu:100", got, err)
		}
		got, err = parseCgroupCPUCSS(overheadPath)
		if err != nil || got[subsystem.SubsystemCPU] != 101 {
			t.Errorf("overhead CPU CSS = %v, err = %v; want cpu:101", got, err)
		}
	})
	t.Run("missing directory", func(t *testing.T) {
		if _, err := parseCgroupCPUCSS("/missing"); err == nil {
			t.Error("missing cgroup must fail")
		}
	})
	t.Run("conflicting CPU addresses", func(t *testing.T) {
		value, _ := cgroupCssMetaDataMap.Load(uint64(100))
		conflict := *value.(*containerCssMetaData)
		conflict.CSS = 300
		cgroupCssMetaDataMap.Store(conflict.CSS, &conflict)
		defer cgroupCssMetaDataMap.Delete(conflict.CSS)
		if _, err := parseCgroupCPUCSS(vmPath); err == nil {
			t.Error("ambiguous cgroup identity must fail")
		}
	})
	t.Run("deleted cgroup cannot reuse cached CSS", func(t *testing.T) {
		value, _ := cgroupCssMetaDataMap.Load(uint64(100))
		event := containerCssPerfEvent{Inode: value.(*containerCssMetaData).Inode}
		event.CSS[0], event.CSS[1] = 100, 200
		copy(event.KnodeName[:len(event.KnodeName)-1], filepath.Base(vmPath))
		if err := cgroupDeleteCssData(&event, extractContainerID(string(event.KnodeName[:]))); err != nil {
			t.Fatal(err)
		}
		if _, err := parseCgroupCPUCSS(vmPath); err == nil {
			t.Error("missing CPU CSS must fail even while the directory still exists")
		}
	})
}

// TestWorkloadCgroupCSSBinding 验证数字目录按控制器和 inode 绑定 CSS，忽略
// 来源自带的地址；CSS 冲突只禁用对应控制器，目录不可用时不发布缓存地址。
func TestWorkloadCgroupCSSBinding(t *testing.T) {
	root := t.TempDir()
	membership := "/cube_sandbox/sandbox/8"
	mounts := []*promprocfs.MountInfo{
		{FSType: "cgroup", Root: "/", MountPoint: filepath.Join(root, "cpu"), SuperOptions: map[string]string{"cpu": ""}},
		{FSType: "cgroup", Root: "/", MountPoint: filepath.Join(root, "memory"), SuperOptions: map[string]string{"memory": ""}},
	}
	locations := CgroupBinding{
		Controllers: map[string]string{"cpu": membership, "memory": membership},
		CSS:         map[string]uint64{"cpu": 8800},
	}
	metadata := make(map[string]*containerCssMetaData)
	for index, controller := range []string{"cpu", "memory"} {
		directory := filepath.Join(mounts[index].MountPoint, membership)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Errorf("create %s directory: %v", controller, err)
			return
		}
		var stat syscall.Stat_t
		if err := syscall.Stat(directory, &stat); err != nil {
			t.Errorf("stat %s directory: %v", controller, err)
			return
		}
		entry := &containerCssMetaData{CSS: uint64(8100 + index), Inode: stat.Ino, SubSys: controller}
		metadata[controller] = entry
		cgroupCssMetaDataMap.Store(entry.CSS, entry)
		t.Cleanup(func() { cgroupCssMetaDataMap.Delete(entry.CSS) })
	}
	binding := bindCgroups(locations, mounts)
	for controller, entry := range metadata {
		if got := binding.CSS[controller]; got != entry.CSS {
			t.Errorf("%s CSS=%d, want %d", controller, got, entry.CSS)
		}
	}
	conflict := *metadata["cpu"]
	conflict.CSS = 8900
	cgroupCssMetaDataMap.Store(conflict.CSS, &conflict)
	t.Cleanup(func() { cgroupCssMetaDataMap.Delete(conflict.CSS) })
	binding = bindCgroups(locations, mounts)
	if binding.CSS["cpu"] != 0 || binding.CSS["memory"] != metadata["memory"].CSS {
		t.Errorf("ambiguous CPU affected independent memory binding: %v", binding.CSS)
	}
	if _, err := binding.Directory("cpu"); err != nil {
		t.Errorf("CSS conflict disabled file collection: %v", err)
	}
	if err := os.Remove(filepath.Join(mounts[1].MountPoint, membership)); err != nil {
		t.Errorf("remove memory directory: %v", err)
		return
	}
	binding = bindCgroups(locations, mounts)
	if len(binding.CSS) != 0 {
		t.Errorf("unavailable directories published cached CSS: %v", binding.CSS)
	}
}
