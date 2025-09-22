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

package v1

import (
	"errors"
	"math"
	"syscall"

	"huatuo-bamai/internal/cgroups/paths"
	"huatuo-bamai/internal/cgroups/pids"
	"huatuo-bamai/internal/cgroups/stats"
	"huatuo-bamai/internal/utils/parseutil"

	extv1 "github.com/containerd/cgroups/v3/cgroup1"
	"github.com/opencontainers/runtime-spec/specs-go"
)

var clockTicks = getClockTicks()

const microsecondsInSecond = 1000000

// a typed name for a cgroup subsystem
const (
	subsysDevices   = "devices"
	subsysHugetlb   = "hugetlb"
	subsysFreezer   = "freezer"
	subsysPids      = "pids"
	subsysNetCLS    = "net_cls"
	subsysNetPrio   = "net_prio"
	subsysPerfEvent = "perf_event"
	subsysCpuset    = "cpuset"
	subsysCpu       = "cpu"
	subsysCpuacct   = "cpuacct"
	subsysMemory    = "memory"
	subsysBlkio     = "blkio"
	subsysRdma      = "rdma"
)

type CgroupV1 struct {
	name   string
	cgroup extv1.Cgroup
}

func New() (*CgroupV1, error) {
	return &CgroupV1{
		name: "legacy",
	}, nil
}

func (c *CgroupV1) Name() string {
	return c.name
}

func (c *CgroupV1) NewRuntime(path string, spec *specs.LinuxResources) error {
	cg, err := extv1.New(extv1.StaticPath(path), spec)
	if err != nil {
		return err
	}

	c.cgroup = cg
	return nil
}

func (c *CgroupV1) DeleteRuntime() error {
	rootfs, err := extv1.Load(extv1.RootPath)
	if err != nil {
		return err
	}

	if err := c.cgroup.MoveTo(rootfs); err != nil {
		return err
	}

	return c.cgroup.Delete()
}

func (c *CgroupV1) UpdateRuntime(spec *specs.LinuxResources) error {
	return c.cgroup.Update(spec)
}

func (c *CgroupV1) AddProc(pid uint64) error {
	return c.cgroup.AddProc(pid)
}

func (c *CgroupV1) Pids(path string) ([]int32, error) {
	return pids.Tasks(paths.Path(subsysCpu, path), "tasks")
}

func (c *CgroupV1) CpuUsage(path string) (*stats.CpuUsage, error) {
	statPath := paths.Path(subsysCpu, path, "cpuacct.stat")
	raw, err := parseutil.RawKV(statPath)
	if err != nil {
		return nil, err
	}

	usagePath := paths.Path(subsysCpu, path, "cpuacct.usage")
	usage, err := parseutil.ReadUint(usagePath)
	if err != nil {
		return nil, err
	}

	user := (raw["user"] * microsecondsInSecond) / clockTicks
	system := (raw["system"] * microsecondsInSecond) / clockTicks

	return &stats.CpuUsage{
		User:   user,
		System: system,
		Usage:  usage / 1000,
	}, nil
}

func (c *CgroupV1) CpuStatRaw(path string) (map[string]uint64, error) {
	return parseutil.RawKV(paths.Path(subsysCpu, path, "cpu.stat"))
}

func (c *CgroupV1) CpuQuotaAndPeriod(path string) (*stats.CpuQuota, error) {
	periodPath := paths.Path(subsysCpu, path, "cpu.cfs_period_us")
	period, err := parseutil.ReadUint(periodPath)
	if err != nil {
		return nil, err
	}

	quotaPath := paths.Path(subsysCpu, path, "cpu.cfs_quota_us")
	quota, err := parseutil.ReadInt(quotaPath)
	if err != nil {
		return nil, err
	}

	if quota == -1 {
		return &stats.CpuQuota{
			Quota:  math.MaxUint64,
			Period: period,
		}, nil
	}

	return &stats.CpuQuota{
		Quota:  uint64(quota),
		Period: period,
	}, nil
}

func (c *CgroupV1) MemoryStatRaw(path string) (map[string]uint64, error) {
	return parseutil.RawKV(paths.Path(subsysMemory, path, "memory.stat"))
}

func (c *CgroupV1) MemoryEventRaw(path string) (map[string]uint64, error) {
	events, err := parseutil.RawKV(paths.Path(subsysMemory, path, "memory.events"))
	if err != nil && errors.Is(err, syscall.ENOENT) {
		// didi kernel cgroupv1 support memmory.events
		// so for native cgroupv1, ignore syscall.ENOENT
		return nil, nil
	}

	return events, err
}

func (c *CgroupV1) MemoryUsage(path string) (*stats.MemoryUsage, error) {
	usage, err := parseutil.ReadUint(paths.Path(subsysMemory,
		path, "memory.usage_in_bytes"))
	if err != nil {
		return nil, err
	}

	maxLimited, err := parseutil.ReadUint(paths.Path(subsysMemory,
		path, "memory.limit_in_bytes"))
	if err != nil {
		return nil, err
	}

	return &stats.MemoryUsage{Usage: usage, MaxLimited: maxLimited}, nil
}
