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

package autotracing

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/paths"
)

// This checks real charged memory, the burst decision and the process snapshot.
// It deliberately does not claim to exercise kubelet discovery or persistence.
func TestContainerBurstLiveGrowth(t *testing.T) {
	pid, err := strconv.Atoi(os.Getenv("HUATUO_MEMBURST_WORKER_PID"))
	if err != nil || pid <= 1 {
		t.Skip("set HUATUO_MEMBURST_WORKER_PID to a bounded memory-growth worker")
	}
	manager, err := cgroups.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	membership, err := cgroups.PathsForPID(pid)
	if err != nil {
		t.Fatal(err)
	}
	unified := cgroups.CgroupMode() == cgroups.Unified
	root, group := paths.RootfsDefaultPath, membership.Unified
	if !unified {
		root = filepath.Join(root, "memory")
		group = membership.Controllers["memory"]
	}
	if group == "" || group == "/" {
		t.Fatal("worker must have an isolated memory cgroup")
	}
	dir := filepath.Join(root, group)
	mem, err := readMemInfo(map[string]bool{"MemTotal": true})
	if err != nil {
		t.Fatal(err)
	}
	sample := func() (int, int) {
		t.Helper()
		raw, err := manager.MemoryStatRaw(group)
		if err != nil {
			t.Fatal(err)
		}
		current, limit, err := containerBurstMemory(raw, root, dir, mem["MemTotal"], unified)
		if err != nil {
			t.Fatal(err)
		}
		return current, limit
	}
	baseline, limit := sample()
	if baseline <= 0 || limit > 256*1024 {
		t.Fatalf("expected an initialized worker with <=256 MiB limit: current=%d limit=%d KiB", baseline, limit)
	}
	deadline := time.NewTimer(12 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("bounded worker did not exceed burst thresholds")
		case <-ticker.C:
			current, currentLimit := sample()
			if currentLimit != limit {
				t.Fatal("worker memory limit changed")
			}
			// A two-sample window isolates the detector from the fixture's startup time.
			history := make([]int, 2)
			index, full := 0, false
			recordMemoryBurst(baseline, limit, history, &index, &full, 2, 70)
			if !recordMemoryBurst(current, limit, history, &index, &full, 2, 70) {
				continue
			}
			procs, err := containerMemoryProcesses(dir, 10)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range procs {
				if p.PID == int32(pid) && p.MemSize > 0 {
					t.Logf("real cgroup burst: baseline=%d current=%d limit=%d KiB, snapshot PID=%d RSS=%d", baseline, current, limit, pid, p.MemSize)
					return
				}
			}
			t.Fatalf("worker %d missing from snapshot: %+v", pid, procs)
		}
	}
}
