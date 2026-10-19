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

//go:build linux

package pod

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/paths"
	"github.com/ccfos/huatuo/internal/procfs"
)

func TestPopulateKataContainerWithoutCPUCSS(t *testing.T) {
	mode := cgroups.CgroupMode()
	if mode == cgroups.Unavailable {
		t.Skip("cgroup filesystem is unavailable")
	}

	originalCgroupRoot := paths.RootfsDefaultPath
	originalProcRoot := filepath.Dir(procfs.DefaultPath())
	t.Cleanup(func() {
		paths.RootfsDefaultPath = originalCgroupRoot
		procfs.RootPrefix(originalProcRoot)
	})

	root := t.TempDir()
	procfs.RootPrefix(root)
	paths.RootfsDefaultPath = filepath.Join(root, "cgroup")

	const (
		pid        = 4242
		cgroupPath = "/kata/vm"
	)
	writeKataProcCgroup(t, root, pid, cgroupPath, mode)
	writeKataCPUFiles(t, cgroupPath, mode)

	// Remove any stale metadata for this temporary cgroup so the CSS lookup is
	// guaranteed to miss.
	var stat syscall.Stat_t
	if err := syscall.Stat(filepath.Join(kataCPURoot(mode), cgroupPath), &stat); err != nil {
		t.Fatal(err)
	}
	cgroupCssMetaDataMap.Range(func(key, value any) bool {
		if metadata, ok := value.(*containerCssMetaData); ok && metadata.Inode == stat.Ino {
			cgroupCssMetaDataMap.Delete(key)
		}
		return true
	})

	container := &Container{ID: "sandbox-container", RuntimeType: "io.containerd.kata.v2"}
	info := &containerdRuntimeInfo{PID: pid, SandboxID: "vm"}
	if err := populateKataContainer(container, info); err != nil {
		t.Fatalf("populateKataContainer() error = %v", err)
	}
	if container.InitPid != pid || container.SandboxID != "vm" || container.CgroupPath != cgroupPath {
		t.Fatalf("Kata binding = %+v", container)
	}
	if container.CgroupCss == nil || len(container.CgroupCss) != 0 {
		t.Fatalf("CgroupCss = %v, want empty non-nil map", container.CgroupCss)
	}
}

func kataCPURoot(mode cgroups.Mode) string {
	if mode == cgroups.Legacy || mode == cgroups.Hybrid {
		return filepath.Join(paths.RootfsDefaultPath, "cpu")
	}
	return paths.RootfsDefaultPath
}

func writeKataProcCgroup(t *testing.T, root string, pid int, cgroupPath string, mode cgroups.Mode) {
	t.Helper()

	content := "0::" + cgroupPath + "\n"
	switch mode {
	case cgroups.Legacy:
		content = "2:cpu,cpuacct:" + cgroupPath + "\n3:memory:" + cgroupPath + "\n"
	case cgroups.Hybrid:
		content = "0::/unified\n2:cpu,cpuacct:" + cgroupPath + "\n3:memory:" + cgroupPath + "\n"
	}

	dir := filepath.Join(root, "proc", strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeKataCPUFiles(t *testing.T, cgroupPath string, mode cgroups.Mode) {
	t.Helper()

	files := []string{"cpu.stat", "cpu.max"}
	if mode == cgroups.Legacy || mode == cgroups.Hybrid {
		files = []string{"cpuacct.stat", "cpuacct.usage", "cpu.cfs_quota_us", "cpu.cfs_period_us"}
	}

	dir := filepath.Join(kataCPURoot(mode), cgroupPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("0\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
