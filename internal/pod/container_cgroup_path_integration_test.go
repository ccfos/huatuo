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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	promprocfs "github.com/prometheus/procfs"
)

func TestValidateContainerRefKernelAndRuntimeBindings(t *testing.T) {
	useContainerdForTest(t)
	root := t.TempDir()
	initMu.Lock()
	previousRoot := containerdStateDir
	containerdStateDir = root
	initMu.Unlock()
	t.Cleanup(func() { initMu.Lock(); containerdStateDir = previousRoot; initMu.Unlock() })
	previousView := containerView
	containerView = newTestContainerStore()
	t.Cleanup(func() { containerView = previousView })

	id := strings.Repeat("a", 64)
	directory := filepath.Join(root, "io.containerd.runtime.v2.task", "k8s.io", id)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(directory, "init.pid")
	for _, change := range []string{"unchanged", "runtime pid", "start time", "directory", "generation", "deleted", "unavailable"} {
		t.Run(change, func(t *testing.T) {
			if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
				t.Fatal(err)
			}
			record, err := containerMemoryBinding(&Container{ID: id, InitPid: os.Getpid()})
			if err != nil {
				t.Fatal(err)
			}
			records := map[string]*containerRecord{id: record}
			containerView.commit(containerSnapshot{records: records, isComplete: true}, nil)
			ref := record.ref
			info, err := MemoryCgroupDirectory(ref)
			if err != nil || !os.SameFile(info, record.directory) {
				t.Fatalf("published directory lookup: %v", err)
			}
			switch change {
			case "runtime pid":
				if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid()+1)), 0o600); err != nil {
					t.Fatal(err)
				}
			case "start time", "directory", "generation":
				updated := *record
				if change == "directory" {
					updated.directory, err = os.Stat(t.TempDir())
					if err != nil {
						t.Fatal(err)
					}
				} else {
					updated.startTime++
				}
				containerView.commit(containerSnapshot{records: map[string]*containerRecord{id: &updated}, isComplete: true}, nil)
				if change != "generation" {
					// Use the new reference to exercise live binding validation.
					ref = updated.ref
				}
			case "deleted":
				containerView.commit(containerSnapshot{isComplete: true}, nil)
			case "unavailable":
				containerView.commit(containerSnapshot{records: records}, ErrContainersUnavailable)
			}
			err = ValidateContainerRef(ref)
			if (err == nil) != (change == "unchanged") {
				t.Fatalf("validate %s binding: %v", change, err)
			}
		})
	}
}

func TestKataHostDoesNotPublishApplicationMemoryTarget(t *testing.T) {
	for _, test := range []struct {
		name, runtime, sandbox string
		available              bool
	}{
		{name: "ordinary", runtime: "io.containerd.runc.v2", available: true},
		{name: "Kata Host", runtime: "io.containerd.kata-qemu.v2", sandbox: "orders-vm"},
		{name: "Firecracker Host", runtime: "io.containerd.kata-fc.v2", sandbox: "orders-vm"},
		{name: "Guest", available: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			container := &Container{
				ID:          strings.Repeat("a", 64),
				InitPid:     os.Getpid(),
				RuntimeType: test.runtime,
				SandboxID:   test.sandbox,
				CgroupPath:  "/preserved-vm-cpu-path",
			}
			record, err := containerMemoryBinding(container)
			if err != nil {
				t.Fatal(err)
			}
			if got := record.ref.MemoryCgroupPath != ""; got != test.available {
				t.Errorf("memory target available=%t, want %t", got, test.available)
			}
			if record.ref.InitPID != container.InitPid ||
				container.CgroupPath != "/preserved-vm-cpu-path" ||
				record.directory == nil {
				t.Error("memory exclusion changed process or CPU identity")
			}
		})
	}
}

// TestWorkloadCgroupMountBindings 验证无代表进程的对象按实际挂载绑定资源，
// 覆盖 v1 分挂载和合并挂载、v2、混合模式、子树挂载及带空格的挂载点。
// 缺少单个控制器和 CSS 不影响其他目录，空路径、根路径及越界路径不可采集。
func TestWorkloadCgroupMountBindings(t *testing.T) {
	cases := []struct {
		name        string
		mounts      []*promprocfs.MountInfo
		locations   CgroupBinding
		directories []string
		want        map[string]string
		unavailable []string
	}{
		{
			name: "separate v1 memberships",
			mounts: []*promprocfs.MountInfo{
				{FSType: "cgroup", Root: "/", MountPoint: "processor", SuperOptions: map[string]string{"cpu": ""}},
				{FSType: "cgroup", Root: "/", MountPoint: "accounting", SuperOptions: map[string]string{"cpuacct": ""}},
				{FSType: "cgroup", Root: "/", MountPoint: "ram", SuperOptions: map[string]string{"memory": ""}},
			},
			locations:   CgroupBinding{Controllers: map[string]string{"cpu": "/checkout-cpu", "cpuacct": "/checkout-accounting", "memory": "/checkout-memory", "blkio": "/checkout-io"}},
			directories: []string{"processor/checkout-cpu", "accounting/checkout-accounting", "ram/checkout-memory"},
			want:        map[string]string{"cpu": "processor/checkout-cpu", "cpuacct": "accounting/checkout-accounting", "memory": "ram/checkout-memory"},
			unavailable: []string{"blkio", "pids"},
		},
		{
			name:        "combined v1 mount",
			mounts:      []*promprocfs.MountInfo{{FSType: "cgroup", Root: "/", MountPoint: "cpu,cpuacct", SuperOptions: map[string]string{"cpu": "", "cpuacct": ""}}},
			locations:   CgroupBinding{Controllers: map[string]string{"cpu": "/cube_sandbox/sandbox/8", "cpuacct": "/cube_sandbox/sandbox/8", "memory": "/cube_sandbox/sandbox/8"}},
			directories: []string{"cpu,cpuacct/cube_sandbox/sandbox/8"},
			want:        map[string]string{"cpu": "cpu,cpuacct/cube_sandbox/sandbox/8", "cpuacct": "cpu,cpuacct/cube_sandbox/sandbox/8"},
			unavailable: []string{"memory"},
		},
		{
			name:        "unified v2 mount",
			mounts:      []*promprocfs.MountInfo{{FSType: "cgroup2", Root: "/", MountPoint: "unified"}},
			locations:   CgroupBinding{Unified: "/cube_sandbox/sandbox/8"},
			directories: []string{"unified/cube_sandbox/sandbox/8"},
			want:        map[string]string{"cpu": "unified/cube_sandbox/sandbox/8", "memory": "unified/cube_sandbox/sandbox/8"},
		},
		{
			name:        "hybrid does not substitute unified for pending memory",
			mounts:      []*promprocfs.MountInfo{{FSType: "cgroup2", Root: "/", MountPoint: "unified"}},
			locations:   CgroupBinding{Unified: "/checkout", Controllers: map[string]string{"memory": "/checkout-memory"}},
			directories: []string{"unified/checkout"},
			want:        map[string]string{"cpu": "unified/checkout"},
			unavailable: []string{"memory"},
		},
		{
			name:        "delegated subtree",
			mounts:      []*promprocfs.MountInfo{{FSType: "cgroup", Root: "/cube_sandbox", MountPoint: "delegated", SuperOptions: map[string]string{"memory": ""}}},
			locations:   CgroupBinding{Controllers: map[string]string{"memory": "/cube_sandbox/sandbox/8"}},
			directories: []string{"delegated/sandbox/8"},
			want:        map[string]string{"memory": "delegated/sandbox/8"},
		},
		{
			name:        "escaped mount path",
			mounts:      []*promprocfs.MountInfo{{FSType: "cgroup2", Root: "/", MountPoint: `mounted\040groups`}},
			locations:   CgroupBinding{Unified: "/checkout"},
			directories: []string{"mounted groups/checkout"},
			want:        map[string]string{"cpu": "mounted groups/checkout"},
		},
		{
			name: "unsafe or absent memberships",
			mounts: []*promprocfs.MountInfo{
				{FSType: "cgroup2", Root: "/", MountPoint: "unified"},
				{FSType: "cgroup", Root: "/", MountPoint: "legacy", SuperOptions: map[string]string{"cpu": "", "memory": "", "blkio": ""}},
			},
			locations:   CgroupBinding{Unified: "/", Controllers: map[string]string{"cpu": "", "memory": "../host", "blkio": "/checkout/../host"}},
			directories: []string{"unified", "legacy/host"},
			unavailable: []string{"cpu", "memory", "blkio"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, mount := range tc.mounts {
				mount.MountPoint = filepath.Join(root, mount.MountPoint)
			}
			for _, directory := range tc.directories {
				if err := os.MkdirAll(filepath.Join(root, directory), 0o700); err != nil {
					t.Errorf("prepare controller directory: %v", err)
					return
				}
			}
			workload := &Pod{ID: "checkout-sandbox", Type: TypeCubeSandbox, Cgroups: bindCgroups(tc.locations, tc.mounts)}
			for controller, suffix := range tc.want {
				directory, err := workload.Cgroups.Directory(controller)
				if err != nil || directory != filepath.Join(root, suffix) {
					t.Errorf("%s directory=%q, err=%v", controller, directory, err)
				}
				membership, available := workload.Cgroups.Path(controller)
				if !available || membership == "" || membership == "/" {
					t.Errorf("%s membership=%q available=%t", controller, membership, available)
				}
			}
			for _, controller := range tc.unavailable {
				if directory, err := workload.Cgroups.Directory(controller); err == nil || directory != "" {
					t.Errorf("unavailable %s returned %q, err=%v", controller, directory, err)
				}
				if membership, available := workload.Cgroups.Path(controller); available || membership != "" {
					t.Errorf("unavailable %s membership=%q", controller, membership)
				}
			}
			if workload.Process != nil || workload.Network != nil || len(workload.Cgroups.CSS) != 0 {
				t.Error("cgroup-only binding fabricated process, network, or CSS")
			}
		})
	}
}

// TestWorkloadCgroupDirectoryReadiness 验证尚未创建的目录不可采集，重新绑定后
// 可读取；目录消失、变成普通文件或符号链接时，旧绑定立即返回不可用。
func TestWorkloadCgroupDirectoryReadiness(t *testing.T) {
	root := t.TempDir()
	mounts := []*promprocfs.MountInfo{{FSType: "cgroup2", Root: "/", MountPoint: root}}
	locations := CgroupBinding{Unified: "/checkout"}
	pending := bindCgroups(locations, mounts)
	if _, err := pending.ProcessDirectory(); err == nil {
		t.Error("pending cgroup resolved a process directory")
	}
	directory := filepath.Join(root, "checkout")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Errorf("create delayed cgroup: %v", err)
		return
	}
	bound := bindCgroups(locations, mounts)
	if got, err := bound.ProcessDirectory(); err != nil || got != directory {
		t.Errorf("ready process directory=%q, err=%v", got, err)
	}
	if err := os.Remove(directory); err != nil {
		t.Errorf("remove cgroup: %v", err)
		return
	}
	if _, err := bound.Directory("cpu"); err == nil {
		t.Error("removed cgroup remains readable")
	}
	if err := os.WriteFile(directory, nil, 0o600); err != nil {
		t.Errorf("create non-directory: %v", err)
		return
	}
	if _, err := bound.Directory("cpu"); err == nil {
		t.Error("ordinary file accepted as cgroup directory")
	}
	if err := os.Remove(directory); err != nil {
		t.Errorf("remove ordinary file: %v", err)
		return
	}
	if err := os.Symlink(root, directory); err != nil {
		t.Errorf("create root symlink: %v", err)
		return
	}
	if _, err := bound.Directory("cpu"); err == nil {
		t.Error("symlink to host root accepted as cgroup directory")
	}
}

// TestWorkloadCgroupV2MountIntegration 在独立挂载命名空间创建真正的 cgroup v2
// 挂载和子目录，验证动态发现、cgroup-only 文件访问和删除后停止采集。
// 不向该组迁移进程，不修改宿主机资源限制。
func TestWorkloadCgroupV2MountIntegration(t *testing.T) {
	const childEnvironment = "HUATUO_TEST_WORKLOAD_CGROUP_V2"
	if os.Getenv(childEnvironment) != "1" {
		command := exec.Command("unshare", "--mount", "--propagation", "private", os.Args[0],
			"-test.run=^TestWorkloadCgroupV2MountIntegration$", "-test.v")
		command.Env = append(os.Environ(), childEnvironment+"=1")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Errorf("private cgroup v2 test: %v\n%s", err, output)
		} else {
			t.Log(string(output))
		}
		return
	}
	root := t.TempDir()
	if err := syscall.Mount("none", root, "cgroup2", 0, ""); err != nil {
		t.Errorf("mount private cgroup v2: %v", err)
		return
	}
	defer func() {
		if err := syscall.Unmount(root, 0); err != nil {
			t.Errorf("unmount private cgroup v2: %v", err)
		}
	}()
	membership := fmt.Sprintf("/huatuo-workload-%d", os.Getpid())
	directory := filepath.Join(root, membership)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Errorf("create cgroup: %v", err)
		return
	}
	defer os.Remove(directory)
	mounts, err := cgroupMounts()
	if err != nil {
		t.Errorf("read kernel mounts: %v", err)
		return
	}
	binding := bindCgroups(CgroupBinding{Unified: membership}, mounts)
	got, err := binding.ProcessDirectory()
	if err != nil || got != directory {
		t.Errorf("kernel cgroup directory=%q, err=%v", got, err)
		return
	}
	if _, err := os.ReadFile(filepath.Join(got, "cgroup.procs")); err != nil {
		t.Errorf("read bound cgroup processes: %v", err)
	}
	if err := os.Remove(directory); err != nil {
		t.Errorf("remove cgroup: %v", err)
		return
	}
	if _, err := binding.ProcessDirectory(); err == nil {
		t.Error("deleted kernel cgroup remains usable")
	}
}

// TestWorkloadCgroupV1MountIntegration 在实际 v1 CPU 与 memory 挂载下创建
// 空测试组，验证控制器目录分别解析；不迁移进程或修改限制，结束后删除测试组。
func TestWorkloadCgroupV1MountIntegration(t *testing.T) {
	mounts, err := cgroupMounts()
	if err != nil {
		t.Errorf("read kernel mounts: %v", err)
		return
	}
	selected := make(map[string]*promprocfs.MountInfo)
	for _, controller := range []string{"cpu", "memory"} {
		for _, mount := range mounts {
			if _, exists := mount.SuperOptions[controller]; mount.FSType == "cgroup" && exists {
				selected[controller] = mount
				break
			}
		}
		if selected[controller] == nil {
			t.Skipf("host has no v1 %s mount", controller)
		}
	}
	locations := CgroupBinding{Controllers: make(map[string]string)}
	directories := make(map[string]string)
	for controller, mount := range selected {
		name := fmt.Sprintf("huatuo-workload-%d", os.Getpid())
		directory := filepath.Join(mountPathUnescape.Replace(mount.MountPoint), name)
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Errorf("create %s cgroup: %v", controller, err)
			return
		}
		t.Cleanup(func() {
			if err := os.Remove(directory); err != nil {
				t.Errorf("remove %s cgroup: %v", controller, err)
			}
		})
		locations.Controllers[controller] = filepath.Join(mountPathUnescape.Replace(mount.Root), name)
		directories[controller] = directory
	}
	binding := bindCgroups(locations, mounts)
	for controller, want := range directories {
		got, err := binding.Directory(controller)
		if err != nil || got != want {
			t.Errorf("%s cgroup directory=%q, err=%v", controller, got, err)
			continue
		}
		if _, err := os.ReadFile(filepath.Join(got, "cgroup.procs")); err != nil {
			t.Errorf("read %s cgroup processes: %v", controller, err)
		}
	}
}
