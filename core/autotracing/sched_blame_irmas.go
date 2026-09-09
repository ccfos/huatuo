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
	schedBlameIrmasCPUStatFile  = "cpu.stat"
	schedBlameIrmasCPUUsageFile = "cpuacct.usage"
)

type schedBlameIrmasCounters struct {
	hierarchyWait uint64
	innerWait     uint64
	throttleWait  uint64
	cpuUsage      uint64
}

type schedBlameIrmasSample struct {
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

type schedBlameIrmasFiles struct {
	cpuStat string
	usage   string
}

type schedBlameIrmasBaseline struct {
	cgroupPath string
	cgid       uint64
	files      schedBlameIrmasFiles
	counters   schedBlameIrmasCounters
}

type schedBlameIrmasSampler struct {
	root      string
	baselines map[string]schedBlameIrmasBaseline
}

func newSchedBlameIrmasSampler() *schedBlameIrmasSampler {
	return &schedBlameIrmasSampler{
		root:      cgroups.RootfsDefaultPath(),
		baselines: make(map[string]schedBlameIrmasBaseline),
	}
}

func (sampler *schedBlameIrmasSampler) resolveFiles(
	cgroupPath string,
) (schedBlameIrmasFiles, error) {
	relativePath := strings.TrimPrefix(
		filepath.Clean(cgroupPath),
		string(filepath.Separator),
	)
	roots := [...]string{"cpu", "cpu,cpuacct", "cpuacct,cpu"}
	for _, subsystem := range roots {
		base := filepath.Join(sampler.root, subsystem, relativePath)
		files := schedBlameIrmasFiles{
			cpuStat: filepath.Join(base, schedBlameIrmasCPUStatFile),
			usage:   filepath.Join(base, schedBlameIrmasCPUUsageFile),
		}
		if _, err := os.Stat(files.cpuStat); err != nil {
			continue
		}
		if _, err := os.Stat(files.usage); err != nil {
			continue
		}
		return files, nil
	}
	return schedBlameIrmasFiles{}, fmt.Errorf(
		"cgroup files not found for %q",
		cgroupPath,
	)
}

func readSchedBlameIrmasCounters(
	files schedBlameIrmasFiles,
) (schedBlameIrmasCounters, error) {
	stat, err := parseutil.RawKV(files.cpuStat)
	if err != nil {
		return schedBlameIrmasCounters{}, fmt.Errorf("read cpu.stat: %w", err)
	}
	required := [...]string{
		"hierarchy_wait_sum",
		"inner_wait_sum",
		"throttle_wait_sum",
	}
	for _, name := range required {
		if _, exists := stat[name]; !exists {
			return schedBlameIrmasCounters{}, fmt.Errorf(
				"cpu.stat missing %s",
				name,
			)
		}
	}
	usage, err := parseutil.ReadUint(files.usage)
	if err != nil {
		return schedBlameIrmasCounters{}, fmt.Errorf(
			"read cpuacct.usage: %w",
			err,
		)
	}
	return schedBlameIrmasCounters{
		hierarchyWait: stat["hierarchy_wait_sum"],
		innerWait:     stat["inner_wait_sum"],
		throttleWait:  stat["throttle_wait_sum"],
		cpuUsage:      usage,
	}, nil
}

func schedBlameIrmasCounterDeltas(
	previous, current schedBlameIrmasCounters,
) (schedBlameIrmasCounters, string) {
	if current.hierarchyWait < previous.hierarchyWait {
		return schedBlameIrmasCounters{}, "hierarchy_wait_decreased"
	}
	if current.innerWait < previous.innerWait {
		return schedBlameIrmasCounters{}, "inner_wait_decreased"
	}
	if current.throttleWait < previous.throttleWait {
		return schedBlameIrmasCounters{}, "throttle_wait_decreased"
	}
	if current.cpuUsage < previous.cpuUsage {
		return schedBlameIrmasCounters{}, "cpu_usage_decreased"
	}
	return schedBlameIrmasCounters{
		hierarchyWait: current.hierarchyWait - previous.hierarchyWait,
		innerWait:     current.innerWait - previous.innerWait,
		throttleWait:  current.throttleWait - previous.throttleWait,
		cpuUsage:      current.cpuUsage - previous.cpuUsage,
	}, ""
}

func calculateSchedBlameIrmasSample(
	previous, current schedBlameIrmasCounters,
) schedBlameIrmasSample {
	deltas, invalidReason := schedBlameIrmasCounterDeltas(previous, current)
	if invalidReason != "" {
		return schedBlameIrmasSample{invalidReason: invalidReason}
	}
	if deltas.hierarchyWait < deltas.innerWait ||
		deltas.hierarchyWait-deltas.innerWait < deltas.throttleWait {
		return schedBlameIrmasSample{
			invalidReason: "hierarchy_less_than_inner_plus_throttle",
		}
	}
	externalWait := deltas.hierarchyWait -
		deltas.innerWait -
		deltas.throttleWait
	if deltas.hierarchyWait > math.MaxUint64-deltas.cpuUsage {
		return schedBlameIrmasSample{
			invalidReason: "total_runnable_demand_overflow",
		}
	}
	totalDemand := deltas.hierarchyWait + deltas.cpuUsage
	sample := schedBlameIrmasSample{
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

func (sampler *schedBlameIrmasSampler) sample(
	containerID, cgroupPath string,
	cgid uint64,
) schedBlameIrmasSample {
	baseline, exists := sampler.baselines[containerID]
	if !exists || baseline.cgroupPath != cgroupPath || baseline.cgid != cgid {
		files, err := sampler.resolveFiles(cgroupPath)
		if err != nil {
			delete(sampler.baselines, containerID)
			return schedBlameIrmasSample{
				invalidReason: "resolve_cgroup: " + err.Error(),
			}
		}
		current, err := readSchedBlameIrmasCounters(files)
		if err != nil {
			delete(sampler.baselines, containerID)
			return schedBlameIrmasSample{
				invalidReason: "read_counters: " + err.Error(),
			}
		}
		sampler.baselines[containerID] = schedBlameIrmasBaseline{
			cgroupPath: cgroupPath,
			cgid:       cgid,
			files:      files,
			counters:   current,
		}
		return schedBlameIrmasSample{invalidReason: "baseline"}
	}

	current, err := readSchedBlameIrmasCounters(baseline.files)
	if err != nil {
		delete(sampler.baselines, containerID)
		return schedBlameIrmasSample{
			invalidReason: "read_counters: " + err.Error(),
		}
	}
	sample := calculateSchedBlameIrmasSample(baseline.counters, current)
	baseline.counters = current
	sampler.baselines[containerID] = baseline
	return sample
}

func (sampler *schedBlameIrmasSampler) retain(
	activeContainerIDs map[string]struct{},
) {
	for containerID := range sampler.baselines {
		if _, active := activeContainerIDs[containerID]; !active {
			delete(sampler.baselines, containerID)
		}
	}
}
