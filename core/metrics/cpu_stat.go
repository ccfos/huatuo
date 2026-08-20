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

package collector

import (
	"math"
	"reflect"
	"sync"
	"time"

	"huatuo-bamai/internal/cgroups"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

type cpuStat struct {
	nrThrottled   uint64
	throttledTime uint64
	nrBursts      uint64
	burstTime     uint64

	// calculated values
	waitSum     uint64
	cpuTotal    uint64
	waitPercent float64

	lastUpdate time.Time
}

type cpuStatAvailability struct {
	waitPercent   bool
	nrThrottled   bool
	throttledTime bool
	nrBursts      bool
	burstTime     bool
}

type cpuStatCollector struct {
	cgroup cgroups.Cgroup
	mutex  sync.Mutex
}

func init() {
	tracing.RegisterEventTracing("cpu_stat", newCPUStat)
	_ = pod.RegisterContainerLifeResources("collector_cpu_stat", reflect.TypeOf(&cpuStat{}))
}

func newCPUStat() (*tracing.EventTracingAttr, error) {
	cgroup, err := cgroups.NewManager()
	if err != nil {
		return nil, err
	}

	return &tracing.EventTracingAttr{
		TracingData: &cpuStatCollector{
			cgroup: cgroup,
		},
		Flag: tracing.FlagMetric,
	}, nil
}

func durationNanoseconds(raw map[string]uint64, nanosecondKey, microsecondKey string) (uint64, bool) {
	if value, ok := raw[nanosecondKey]; ok {
		return value, true
	}

	value, ok := raw[microsecondKey]
	if !ok || value > math.MaxUint64/1000 {
		return 0, false
	}

	return value * 1000, true
}

func calculateWaitPercent(current, previous *cpuStat) (float64, bool) {
	// Live-cgroup counters are normally monotonic; guard rare resets or
	// wraparound to prevent unsigned underflow.
	if current.waitSum < previous.waitSum ||
		current.cpuTotal < previous.cpuTotal {
		return 0, false
	}

	deltaWait := current.waitSum - previous.waitSum
	deltaCPU := current.cpuTotal - previous.cpuTotal
	if deltaWait == 0 && deltaCPU == 0 {
		return 0, true
	}

	return float64(deltaWait) * 100 / (float64(deltaWait) + float64(deltaCPU)), true
}

func newCPUStatSample(raw map[string]uint64, cpuTotal uint64, now time.Time) (cpuStat, cpuStatAvailability) {
	nrThrottled, nrThrottledOK := raw["nr_throttled"]
	throttledTime, throttledTimeOK := durationNanoseconds(raw, "throttled_time", "throttled_usec")
	waitSum, waitSumOK := raw["wait_sum"]
	nrBursts, nrBurstsOK := raw["nr_bursts"]
	burstTime, burstTimeOK := durationNanoseconds(raw, "burst_time", "burst_usec")

	return cpuStat{
			nrThrottled:   nrThrottled,
			throttledTime: throttledTime,
			waitSum:       waitSum,
			nrBursts:      nrBursts,
			burstTime:     burstTime,
			cpuTotal:      cpuTotal,
			lastUpdate:    now,
		}, cpuStatAvailability{
			waitPercent:   waitSumOK,
			nrThrottled:   nrThrottledOK,
			throttledTime: throttledTimeOK,
			nrBursts:      nrBurstsOK,
			burstTime:     burstTimeOK,
		}
}

func (c *cpuStatCollector) updateDataCache(cpu *cpuStat, container *pod.Container) (cpuStatAvailability, error) {
	var availability cpuStatAvailability

	c.mutex.Lock()
	defer c.mutex.Unlock()

	now := time.Now()
	if now.Sub(cpu.lastUpdate) < time.Second {
		return availability, nil
	}

	raw, err := c.cgroup.CpuStatRaw(container.CgroupPath)
	if err != nil {
		return availability, err
	}

	usage, err := c.cgroup.CpuUsage(container.CgroupPath)
	if err != nil {
		return availability, err
	}

	stat, availability := newCPUStatSample(raw, usage.Usage*1000, now)
	if !availability.waitPercent {
		stat.lastUpdate = time.Time{}
		*cpu = stat
		return availability, nil
	}

	if cpu.lastUpdate.IsZero() {
		availability.waitPercent = false
		*cpu = stat
		return availability, nil
	}

	stat.waitPercent, availability.waitPercent = calculateWaitPercent(&stat, cpu)

	*cpu = stat
	return availability, nil
}

func (c *cpuStatCollector) Update() ([]*metric.Data, error) {
	metrics := []*metric.Data{}

	containers, err := pod.ContainersByType(pod.ContainerTypeNormal | pod.ContainerTypeSidecar)
	if err != nil {
		return nil, err
	}

	for _, container := range containers {
		dataCache, ok := container.LifeResources("collector_cpu_stat").(*cpuStat)
		if !ok || dataCache == nil {
			log.Warnf("cpu_stat: LifeResources for container %s returned unexpected type or nil", container)
			continue
		}
		available, err := c.updateDataCache(dataCache, container)
		if err != nil {
			log.Infof("failed to update cpu info of %s, %v", container, err)
			continue
		}

		if available.waitPercent {
			metrics = append(metrics, metric.NewContainerGaugeData(
				container,
				"wait_sum_percent",
				dataCache.waitPercent,
				"CFS cgroup runqueue wait as a percentage of total schedulable time (requires kernel.sched_schedstats=1)",
				nil,
			))
		}
		if available.nrThrottled {
			metrics = append(metrics, metric.NewContainerGaugeData(
				container,
				"nr_throttled",
				float64(dataCache.nrThrottled),
				"number of CFS periods in which the cgroup was throttled",
				nil,
			))
		}
		if available.throttledTime {
			metrics = append(metrics, metric.NewContainerGaugeData(
				container,
				"throttled_time",
				float64(dataCache.throttledTime),
				"total CFS cgroup throttled time in nanoseconds",
				nil,
			))
		}
		if available.nrBursts {
			metrics = append(metrics, metric.NewContainerGaugeData(
				container,
				"nr_bursts",
				float64(dataCache.nrBursts),
				"number of CFS periods in which the cgroup used burst capacity",
				nil,
			))
		}
		if available.burstTime {
			metrics = append(metrics, metric.NewContainerGaugeData(
				container,
				"burst_time",
				float64(dataCache.burstTime),
				"total CFS cgroup burst time in nanoseconds",
				nil,
			))
		}
	}

	return metrics, nil
}
