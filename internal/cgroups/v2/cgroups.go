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

package v2

import (
	"fmt"
	"math"
	"strconv"

	"huatuo-bamai/internal/cgroups/paths"
	"huatuo-bamai/internal/cgroups/pids"
	"huatuo-bamai/internal/cgroups/stats"
	"huatuo-bamai/internal/utils/parseutil"

	extv2 "github.com/containerd/cgroups/v3/cgroup2"
	"github.com/opencontainers/runtime-spec/specs-go"
)

type CgroupV2 struct {
	name   string
	cgroup *extv2.Manager
}

func New() (*CgroupV2, error) {
	return &CgroupV2{
		name: "unified",
	}, nil
}

func (c *CgroupV2) Name() string {
	return c.name
}

func (c *CgroupV2) NewRuntime(path string, spec *specs.LinuxResources) error {
	m, err := extv2.NewSystemd("/", path+".slice", -1, extv2.ToResources(spec))
	if err != nil {
		return fmt.Errorf("cgroup2 new systemd: %w", err)
	}

	// enable cpu and memory cgroup controllers
	if err := m.ToggleControllers([]string{"cpu", "memory"}, extv2.Enable); err != nil {
		_ = m.DeleteSystemd()
		return fmt.Errorf("cgroup2 enabling cpu and memory controllers: %w", err)
	}

	c.cgroup = m
	return nil
}

func (c *CgroupV2) DeleteRuntime() error {
	rootfs, err := extv2.LoadSystemd("/", "")
	if err != nil {
		return err
	}

	if err := c.cgroup.MoveTo(rootfs); err != nil {
		return err
	}

	if err := c.cgroup.Delete(); err != nil {
		return err
	}

	return c.cgroup.DeleteSystemd()
}

func (c *CgroupV2) UpdateRuntime(spec *specs.LinuxResources) error {
	return c.cgroup.Update(extv2.ToResources(spec))
}

func (c *CgroupV2) AddProc(pid uint64) error {
	return c.cgroup.AddProc(pid)
}

func (c *CgroupV2) Pids(path string) ([]int32, error) {
	return pids.Tasks(paths.Path(path), "cgroup.threads")
}

func (c *CgroupV2) CpuStatRaw(path string) (map[string]uint64, error) {
	return parseutil.RawKV(paths.Path(path, "cpu.stat"))
}

func (c *CgroupV2) CpuUsage(path string) (*stats.CpuUsage, error) {
	raw, err := c.CpuStatRaw(path)
	if err != nil {
		return nil, err
	}

	return &stats.CpuUsage{
		Usage:  raw["usage_usec"],
		User:   raw["user_usec"],
		System: raw["system_usec"],
	}, nil
}

func (c *CgroupV2) CpuQuotaAndPeriod(path string) (*stats.CpuQuota, error) {
	maxpath := paths.Path(path, "cpu.max")

	maxQuota, period, err := parseutil.KV(maxpath)
	if err != nil {
		return nil, err
	}

	if maxQuota == "max" {
		return &stats.CpuQuota{Quota: math.MaxUint64, Period: period}, nil
	}

	quota, err := strconv.ParseUint(maxQuota, 10, 64)
	if err != nil {
		return nil, err
	}

	return &stats.CpuQuota{Quota: quota, Period: period}, nil
}

func (c *CgroupV2) MemoryStatRaw(path string) (map[string]uint64, error) {
	return parseutil.RawKV(paths.Path(path, "memory.stat"))
}

func (c *CgroupV2) MemoryEventRaw(path string) (map[string]uint64, error) {
	return parseutil.RawKV(paths.Path(path, "memory.events"))
}

func (c *CgroupV2) MemoryUsage(path string) (*stats.MemoryUsage, error) {
	usage, err := parseutil.ReadUint(paths.Path(path, "memory.current"))
	if err != nil {
		return nil, err
	}

	maxLimited, err := parseutil.ReadUint(paths.Path(path, "memory.max"))
	if err != nil {
		return nil, err
	}

	return &stats.MemoryUsage{Usage: usage, MaxLimited: maxLimited}, nil
}
