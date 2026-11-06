// Copyright 2025, 2026 The HuaTuo Authors
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
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/ccfos/huatuo/internal/cgroups/paths"
	"github.com/ccfos/huatuo/internal/cgroups/pids"
	"github.com/ccfos/huatuo/internal/cgroups/stats"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	"github.com/ccfos/huatuo/internal/utils/cpuutil"
	"github.com/ccfos/huatuo/internal/utils/parseutil"

	extv1 "github.com/containerd/cgroups/v3/cgroup1"
	"github.com/opencontainers/runtime-spec/specs-go"
)

var clockTicks = getClockTicks()

const microsecondsInSecond = 1000000

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
	return pids.Tasks(paths.Path(subsystem.SubsystemCPU, path), "tasks")
}

func (c *CgroupV1) Procs(path string) ([]int32, error) {
	return pids.Tasks(paths.Path(subsystem.SubsystemCPU, path), "cgroup.procs")
}

func (c *CgroupV1) CpuUsage(path string) (*stats.CpuUsage, error) {
	return ReadCPUUsage(paths.Path(subsystem.SubsystemCPUAcct, path))
}

// ReadCPUUsage reads an already resolved cpuacct directory.
func ReadCPUUsage(directory string) (*stats.CpuUsage, error) {
	statPath := filepath.Join(directory, "cpuacct.stat")
	raw, err := parseutil.RawKV(statPath)
	if err != nil {
		return nil, err
	}

	usagePath := filepath.Join(directory, "cpuacct.usage")
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
	return ReadCPUStat(paths.Path(subsystem.SubsystemCPU, path))
}

// ReadCPUStat reads counters from an already resolved cpu directory.
func ReadCPUStat(directory string) (map[string]uint64, error) {
	f, err := os.Open(filepath.Join(directory, "cpu.stat"))
	if err != nil {
		return nil, err
	}
	defer f.Close()

	raw := make(map[string]uint64)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		parts := strings.Fields(scanner.Text())
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid cpu.stat line: %q", scanner.Text())
		}

		value, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			// Vendor kernels may expose negative values that are not usable counters.
			signed, signedErr := strconv.ParseInt(parts[1], 10, 64)
			if signedErr == nil && signed < 0 {
				delete(raw, parts[0])
				continue
			}

			return nil, fmt.Errorf("parse cpu.stat field %q: %w", parts[0], err)
		}

		raw[parts[0]] = value
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return raw, nil
}

func (c *CgroupV1) CpuQuotaAndPeriod(path string) (*stats.CpuQuota, error) {
	return ReadCPUQuota(paths.Path(subsystem.SubsystemCPU, path), paths.Path(subsystem.SubsystemCPUSet, path, "cpuset.cpus"))
}

// ReadCPUQuota accepts an independent cpuset file; empty means no cpuset binding.
func ReadCPUQuota(directory, cpusetFile string) (*stats.CpuQuota, error) {
	periodPath := filepath.Join(directory, "cpu.cfs_period_us")
	period, err := parseutil.ReadUint(periodPath)
	if err != nil {
		return nil, err
	}

	quotaPath := filepath.Join(directory, "cpu.cfs_quota_us")
	quota, err := parseutil.ReadInt(quotaPath)
	if err != nil {
		return nil, err
	}

	var effectiveCPUCount uint64
	if cpusetFile != "" {
		effectiveCPUCount, err = cpuutil.ParseOnlineCores(cpusetFile)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}

	if quota == -1 {
		return &stats.CpuQuota{
			Quota:             math.MaxUint64,
			Period:            period,
			EffectiveCPUCount: effectiveCPUCount,
		}, nil
	}

	return &stats.CpuQuota{
		Quota:             uint64(quota),
		Period:            period,
		EffectiveCPUCount: effectiveCPUCount,
	}, nil
}

func (c *CgroupV1) MemoryStatRaw(path string) (map[string]uint64, error) {
	return parseutil.RawKV(paths.Path(subsystem.SubsystemMemory, path, "memory.stat"))
}

func (c *CgroupV1) MemoryEventRaw(path string) (map[string]uint64, error) {
	events, err := parseutil.RawKV(paths.Path(subsystem.SubsystemMemory, path, "memory.events"))
	if err != nil && errors.Is(err, syscall.ENOENT) {
		// didi kernel cgroupv1 support memmory.events
		// so for native cgroupv1, ignore syscall.ENOENT
		return nil, nil
	}

	return events, err
}

func (c *CgroupV1) MemoryUsage(path string) (*stats.MemoryUsage, error) {
	usage, err := parseutil.ReadUint(paths.Path(subsystem.SubsystemMemory,
		path, "memory.usage_in_bytes"))
	if err != nil {
		return nil, err
	}

	maxLimited, err := parseutil.ReadUint(paths.Path(subsystem.SubsystemMemory,
		path, "memory.limit_in_bytes"))
	if err != nil {
		return nil, err
	}

	return &stats.MemoryUsage{Usage: usage, MaxLimited: maxLimited}, nil
}
