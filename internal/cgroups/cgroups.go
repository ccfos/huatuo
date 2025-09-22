// Copyright 2025 The HuaTuo Authors
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

package cgroups

import (
	"fmt"
	"path/filepath"

	"huatuo-bamai/internal/cgroups/paths"
	"huatuo-bamai/internal/cgroups/stats"
	v1 "huatuo-bamai/internal/cgroups/v1"
	v2 "huatuo-bamai/internal/cgroups/v2"

	extcgroups "github.com/containerd/cgroups/v3"
	"github.com/opencontainers/runtime-spec/specs-go"
)

var cpuPeriod uint64 = 100000

// Mode is the cgroups mode of the host system
type Mode int

const (
	// Unavailable cgroup mountpoint
	Unavailable Mode = iota
	// Legacy cgroups v1
	Legacy
	// Hybrid with cgroups v1 and v2 controllers mounted
	Hybrid
	// Unified with only cgroups v2 mounted
	Unified
)

type Cgroup interface {
	// Name returns the cgroup name.
	Name() string
	// New a runtime config instance.
	NewRuntime(path string, spec *specs.LinuxResources) error
	// Delete a runtime config
	DeleteRuntime() error
	// Update a runtime config
	UpdateRuntime(spec *specs.LinuxResources) error
	// Add pids to cgroup.procs
	AddProc(pid uint64) error
	// Pids return pids of cgroups
	Pids(path string) ([]int32, error)
	// CpuUsage return cgroups user/system and total usage.
	CpuUsage(path string) (*stats.CpuUsage, error)
	// CpuStatRaw return cpu.stat raw data
	CpuStatRaw(path string) (map[string]uint64, error)
	// CpuQuotaAndPeriod cgroup quota and period
	CpuQuotaAndPeriod(path string) (*stats.CpuQuota, error)
	// MemoryStatRaw memory.stat
	MemoryStatRaw(path string) (map[string]uint64, error)
	// MemoryEventRaw memory.stat
	MemoryEventRaw(path string) (map[string]uint64, error)
	// memory.usage_in_bytes,memory.limit_in_bytes in cgroup1
	// memory.current,memory.max in cgroup2
	MemoryUsage(path string) (*stats.MemoryUsage, error)
}

func NewCgroupManager() (Cgroup, error) {
	// https://github.com/systemd/systemd/blob/main/docs/CGROUP_DELEGATION.md
	//
	// Legacy — this is the traditional cgroup v1 mode. In this mode the various
	// controllers each get their own cgroup file system mounted to /sys/fs/cgroup/<controller>/.
	// On top of that systemd manages its own cgroup hierarchy for managing purposes as /sys/fs/cgroup/systemd/.
	//
	// Hybrid — this is a hybrid between the unified and legacy mode.
	// It's set up mostly like legacy, except that there's also an additional
	// hierarchy /sys/fs/cgroup/unified/ that contains the cgroup v2 hierarchy.
	// (Note that in this mode the unified hierarchy won't have controllers
	// attached, the controllers are all mounted as separate hierarchies as
	// in legacy mode, i.e. /sys/fs/cgroup/unified/ is purely and exclusively
	// about core cgroup v2 functionality and not about resource management.)
	// In this mode compatibility with cgroup v1 is retained while some NewCgroupManager
	// v2 features are available too. This mode is a stopgap.
	// Don't bother with this too much unless you have too much free time.

	switch extcgroups.Mode() {
	case extcgroups.Legacy:
		return v1.New()
	case extcgroups.Unified:
		return v2.New()
	case extcgroups.Hybrid:
		return v1.New()
	default:
		return nil, fmt.Errorf("not supported")
	}
}

func CgroupMode() Mode {
	return Mode(extcgroups.Mode())
}

func ToSpec(cpu float64, memory int64) *specs.LinuxResources {
	spec := &specs.LinuxResources{}

	if cpu != 0 {
		quota := int64(cpu * float64(cpuPeriod))
		spec.CPU = &specs.LinuxCPU{
			Period: &cpuPeriod,
			Quota:  &quota,
		}
	}

	if memory != 0 {
		spec.Memory = &specs.LinuxMemory{Limit: &memory}
	}

	return spec
}

func RootFsFilePath(subsys string) string {
	return filepath.Join(paths.RootfsDefaultPath, subsys)
}

func RootfsDefaultPath() string {
	return paths.RootfsDefaultPath
}
