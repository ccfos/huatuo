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
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/utils/parseutil"
)

const (
	schedBlameSuperMonitorCPUStatFile  = "cpu.stat"
	schedBlameSuperMonitorCPUUsageFile = "cpuacct.usage"
)

type schedBlameSuperMonitorCounters struct {
	hierarchyWait uint64
	innerWait     uint64
	throttleWait  uint64
	cpuUsage      uint64
}

type schedBlameSuperMonitorSample struct {
	valid              bool
	waitrate           float64
	hierarchyWaitDelta uint64
	innerWaitDelta     uint64
	throttleWaitDelta  uint64
	externalWaitDelta  uint64
	cpuUsageDelta      uint64
	totalDemandDelta   uint64
	invalidReason      string
}

type schedBlameSuperMonitorFiles struct {
	cpuStat string
	usage   string
}

type schedBlameSuperMonitorBaseline struct {
	cgroupPath string
	cgid       uint64
	files      schedBlameSuperMonitorFiles
	counters   schedBlameSuperMonitorCounters
}

type schedBlameSuperMonitorSampler struct {
	root      string
	baselines map[string]schedBlameSuperMonitorBaseline
}

func newSchedBlameSuperMonitorSampler() *schedBlameSuperMonitorSampler {
	return &schedBlameSuperMonitorSampler{
		root:      cgroups.RootfsDefaultPath(),
		baselines: make(map[string]schedBlameSuperMonitorBaseline),
	}
}

func (sampler *schedBlameSuperMonitorSampler) resolveFiles(
	cgroupPath string,
) (schedBlameSuperMonitorFiles, error) {
	relativePath := strings.TrimPrefix(
		filepath.Clean(cgroupPath),
		string(filepath.Separator),
	)
	roots := [...]string{"cpu", "cpu,cpuacct", "cpuacct,cpu"}
	for _, subsystem := range roots {
		base := filepath.Join(sampler.root, subsystem, relativePath)
		files := schedBlameSuperMonitorFiles{
			cpuStat: filepath.Join(base, schedBlameSuperMonitorCPUStatFile),
			usage:   filepath.Join(base, schedBlameSuperMonitorCPUUsageFile),
		}
		if _, err := os.Stat(files.cpuStat); err != nil {
			continue
		}
		if _, err := os.Stat(files.usage); err != nil {
			continue
		}
		return files, nil
	}
	return schedBlameSuperMonitorFiles{}, fmt.Errorf(
		"cgroup files not found for %q",
		cgroupPath,
	)
}

func readSchedBlameSuperMonitorCounters(
	files schedBlameSuperMonitorFiles,
) (schedBlameSuperMonitorCounters, error) {
	stat, err := parseutil.RawKV(files.cpuStat)
	if err != nil {
		return schedBlameSuperMonitorCounters{}, fmt.Errorf("read cpu.stat: %w", err)
	}
	required := [...]string{
		"hierarchy_wait_sum",
		"inner_wait_sum",
		"throttle_wait_sum",
	}
	for _, name := range required {
		if _, exists := stat[name]; !exists {
			return schedBlameSuperMonitorCounters{}, fmt.Errorf(
				"cpu.stat missing %s",
				name,
			)
		}
	}
	usage, err := parseutil.ReadUint(files.usage)
	if err != nil {
		return schedBlameSuperMonitorCounters{}, fmt.Errorf(
			"read cpuacct.usage: %w",
			err,
		)
	}
	return schedBlameSuperMonitorCounters{
		hierarchyWait: stat["hierarchy_wait_sum"],
		innerWait:     stat["inner_wait_sum"],
		throttleWait:  stat["throttle_wait_sum"],
		cpuUsage:      usage,
	}, nil
}

func schedBlameSuperMonitorCounterDeltas(
	previous, current schedBlameSuperMonitorCounters,
) (schedBlameSuperMonitorCounters, string) {
	if current.hierarchyWait < previous.hierarchyWait {
		return schedBlameSuperMonitorCounters{}, "hierarchy_wait_decreased"
	}
	if current.innerWait < previous.innerWait {
		return schedBlameSuperMonitorCounters{}, "inner_wait_decreased"
	}
	if current.throttleWait < previous.throttleWait {
		return schedBlameSuperMonitorCounters{}, "throttle_wait_decreased"
	}
	if current.cpuUsage < previous.cpuUsage {
		return schedBlameSuperMonitorCounters{}, "cpu_usage_decreased"
	}
	return schedBlameSuperMonitorCounters{
		hierarchyWait: current.hierarchyWait - previous.hierarchyWait,
		innerWait:     current.innerWait - previous.innerWait,
		throttleWait:  current.throttleWait - previous.throttleWait,
		cpuUsage:      current.cpuUsage - previous.cpuUsage,
	}, ""
}

func calculateSchedBlameSuperMonitorSample(
	previous, current schedBlameSuperMonitorCounters,
) schedBlameSuperMonitorSample {
	deltas, invalidReason := schedBlameSuperMonitorCounterDeltas(previous, current)
	if invalidReason != "" {
		return schedBlameSuperMonitorSample{invalidReason: invalidReason}
	}
	if deltas.hierarchyWait < deltas.innerWait ||
		deltas.hierarchyWait-deltas.innerWait < deltas.throttleWait {
		return schedBlameSuperMonitorSample{
			invalidReason: "hierarchy_less_than_inner_plus_throttle",
		}
	}
	externalWait := deltas.hierarchyWait -
		deltas.innerWait -
		deltas.throttleWait
	if deltas.hierarchyWait > math.MaxUint64-deltas.cpuUsage {
		return schedBlameSuperMonitorSample{
			invalidReason: "total_runnable_demand_overflow",
		}
	}
	totalDemand := deltas.hierarchyWait + deltas.cpuUsage
	sample := schedBlameSuperMonitorSample{
		hierarchyWaitDelta: deltas.hierarchyWait,
		innerWaitDelta:     deltas.innerWait,
		throttleWaitDelta:  deltas.throttleWait,
		externalWaitDelta:  externalWait,
		cpuUsageDelta:      deltas.cpuUsage,
		totalDemandDelta:   totalDemand,
	}
	if totalDemand == 0 {
		sample.invalidReason = "no_runnable_demand"
		return sample
	}
	sample.valid = true
	sample.waitrate = float64(externalWait) / float64(totalDemand)
	return sample
}

func (sampler *schedBlameSuperMonitorSampler) sample(
	containerID, cgroupPath string,
	cgid uint64,
) schedBlameSuperMonitorSample {
	baseline, exists := sampler.baselines[containerID]
	if !exists || baseline.cgroupPath != cgroupPath || baseline.cgid != cgid {
		files, err := sampler.resolveFiles(cgroupPath)
		if err != nil {
			delete(sampler.baselines, containerID)
			return schedBlameSuperMonitorSample{
				invalidReason: "resolve_cgroup: " + err.Error(),
			}
		}
		current, err := readSchedBlameSuperMonitorCounters(files)
		if err != nil {
			delete(sampler.baselines, containerID)
			return schedBlameSuperMonitorSample{
				invalidReason: "read_counters: " + err.Error(),
			}
		}
		sampler.baselines[containerID] = schedBlameSuperMonitorBaseline{
			cgroupPath: cgroupPath,
			cgid:       cgid,
			files:      files,
			counters:   current,
		}
		return schedBlameSuperMonitorSample{invalidReason: "baseline"}
	}

	current, err := readSchedBlameSuperMonitorCounters(baseline.files)
	if err != nil {
		delete(sampler.baselines, containerID)
		return schedBlameSuperMonitorSample{
			invalidReason: "read_counters: " + err.Error(),
		}
	}
	sample := calculateSchedBlameSuperMonitorSample(baseline.counters, current)
	baseline.counters = current
	sampler.baselines[containerID] = baseline
	return sample
}

func (sampler *schedBlameSuperMonitorSampler) retain(
	activeContainerIDs map[string]struct{},
) {
	for containerID := range sampler.baselines {
		if _, active := activeContainerIDs[containerID]; !active {
			delete(sampler.baselines, containerID)
		}
	}
}
