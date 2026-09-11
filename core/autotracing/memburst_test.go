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

package autotracing

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/paths"
)

func TestContainerBurstLiveMemory(t *testing.T) {
	if os.Getenv("HUATUO_TRIGGER_LIVE_MEMORY") != "1" {
		t.Skip("set HUATUO_TRIGGER_LIVE_MEMORY=1 on a test VM")
	}
	manager, err := cgroups.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	membership, err := cgroups.PathsForPID(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	unified := cgroups.CgroupMode() == cgroups.Unified
	root := paths.RootfsDefaultPath
	group := membership.Unified
	if !unified {
		root = filepath.Join(root, "memory")
		group = membership.Controllers["memory"]
	}
	if group == "" {
		t.Fatal("missing memory cgroup")
	}
	raw, err := manager.MemoryStatRaw(group)
	if err != nil {
		t.Fatal(err)
	}
	mem, err := readMemInfo(map[string]bool{"MemTotal": true})
	if err != nil {
		t.Fatal(err)
	}
	current, limit, err := containerBurstMemory(raw, root, filepath.Join(root, group), mem["MemTotal"], unified)
	if err != nil || current <= 0 || limit <= 0 {
		t.Fatal(current, limit, err)
	}
	t.Logf("live memory cgroup %s: anonymous LRU=%d KiB, effective denominator=%d KiB", group, current, limit)
}
