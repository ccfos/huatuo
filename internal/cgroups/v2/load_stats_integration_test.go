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

//go:build integration && linux

package v2

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	huatuoBPF "github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/cgroups/paths"
)

func TestLoadStatsLiveTaskIterator(t *testing.T) {
	bpfDir := os.Getenv("HUATUO_CGROUP_V2_LOAD_BPF_DIR")
	cgroupRoot := os.Getenv("HUATUO_CGROUP_V2_LOAD_ROOT")
	if bpfDir == "" || cgroupRoot == "" {
		t.Skip("run through integration/test_cgroup_v2_load_stats.sh")
	}
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}

	if err := huatuoBPF.Init(nil); err != nil {
		t.Fatalf("bpf.Init() error = %v", err)
	}
	t.Cleanup(huatuoBPF.Shutdown)
	t.Cleanup(func() {
		if err := CloseLoadStats(); err != nil {
			t.Errorf("close cgroup v2 load stats: %v", err)
		}
	})

	previousBPFDir := huatuoBPF.DefaultObjDir
	previousCgroupRoot := paths.RootfsDefaultPath
	huatuoBPF.DefaultObjDir = bpfDir
	paths.RootfsDefaultPath = cgroupRoot
	t.Cleanup(func() {
		huatuoBPF.DefaultObjDir = previousBPFDir
		paths.RootfsDefaultPath = previousCgroupRoot
	})

	fixtureDir, err := os.MkdirTemp(paths.RootfsDefaultPath, "huatuo-load-stats-")
	if err != nil {
		t.Fatalf("create cgroup v2 fixture: %v", err)
	}
	cgroupDirs := []string{
		filepath.Join(fixtureDir, "parent"),
		filepath.Join(fixtureDir, "parent", "child"),
		filepath.Join(fixtureDir, "sibling"),
	}
	for _, cgroupDir := range cgroupDirs {
		if err := os.Mkdir(cgroupDir, 0o755); err != nil {
			t.Fatalf("create cgroup v2 fixture %q: %v", cgroupDir, err)
		}
	}
	t.Cleanup(func() {
		for i := len(cgroupDirs) - 1; i >= 0; i-- {
			if err := os.Remove(cgroupDirs[i]); err != nil &&
				!errors.Is(err, os.ErrNotExist) {
				t.Errorf("remove cgroup v2 fixture %q: %v", cgroupDirs[i], err)
			}
		}
		if err := os.Remove(fixtureDir); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			t.Errorf("remove cgroup v2 fixture %q: %v", fixtureDir, err)
		}
	})

	wantSleeping := []uint64{1, 2, 3}
	cgroupPaths := make([]string, len(cgroupDirs))
	for i, cgroupDir := range cgroupDirs {
		startSleepingWorkers(t, cgroupDir, int(wantSleeping[i]))
		cgroupPaths[i], err = filepath.Rel(paths.RootfsDefaultPath, cgroupDir)
		if err != nil {
			t.Fatalf("resolve fixture cgroup path: %v", err)
		}
	}

	result, err := LoadStats(cgroupPaths)
	if err != nil {
		t.Fatalf("LoadStats(%q) error = %v", cgroupPaths, err)
	}
	for i, cgroupPath := range cgroupPaths {
		load, ok := result[cgroupPath]
		if !ok {
			t.Fatalf("LoadStats(%q) returned no entry", cgroupPath)
		}
		if load.NrSleeping != wantSleeping[i] {
			t.Fatalf("LoadStats(%q) = %+v, want %d sleeping tasks",
				cgroupPath, load, wantSleeping[i])
		}
	}
}

func startSleepingWorkers(t *testing.T, cgroupDir string, count int) {
	t.Helper()
	for range count {
		worker := exec.Command("sleep", "30")
		if err := worker.Start(); err != nil {
			t.Fatalf("start sleeping fixture: %v", err)
		}
		t.Cleanup(func() {
			if worker.Process != nil {
				_ = worker.Process.Kill()
			}
			_ = worker.Wait()
		})

		if err := os.WriteFile(
			filepath.Join(cgroupDir, "cgroup.procs"),
			[]byte(strconv.Itoa(worker.Process.Pid)),
			0,
		); err != nil {
			t.Fatalf("move fixture process into cgroup: %v", err)
		}
		waitForTaskState(t, worker.Process.Pid, 'S')
	}
}

func waitForTaskState(t *testing.T, pid int, want byte) {
	t.Helper()
	statusPath := fmt.Sprintf("/proc/%d/stat", pid)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		stat, err := os.ReadFile(statusPath)
		if err == nil {
			closingParen := strings.LastIndexByte(string(stat), ')')
			if closingParen >= 0 && len(stat) > closingParen+2 &&
				stat[closingParen+2] == want {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d did not enter task state %c", pid, want)
}
