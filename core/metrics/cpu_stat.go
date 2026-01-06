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

package collector

import (
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
	hierarchyWaitSum uint64
	innerWaitSum     uint64
	cpuTotal         uint64

	waitrateHierarchy float64
	waitrateInner     float64
	waitrateExter     float64
	waitrateThrottled float64

	lastUpdate time.Time
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
	cgroup, err := cgroups.NewCgroupManager()
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

func (c *cpuStatCollector) cpuMetricUpdate(cpu *cpuStat, container *pod.Container) error {
	var (
		deltaThrottledSum     uint64
		deltaHierarchyWaitSum uint64
		deltaInnerWaitSum     uint64
		deltaExterWaitSum     uint64
	)

	c.mutex.Lock()
	defer c.mutex.Unlock()

	now := time.Now()
	if now.Sub(cpu.lastUpdate).Nanoseconds() < 1000000000 {
		return nil
	}

	raw, err := c.cgroup.CpuStatRaw(container.CgroupSuffix)
	if err != nil {
		return err
	}

	usage, err := c.cgroup.CpuUsage(container.CgroupSuffix)
	if err != nil {
		return err
	}

	stat := cpuStat{
		nrThrottled:      raw["nr_throttled"],
		throttledTime:    raw["throttled_time"],
		hierarchyWaitSum: raw["hierarchy_wait_sum"],
		innerWaitSum:     raw["inner_wait_sum"],
		nrBursts:         raw["nr_bursts"],
		burstTime:        raw["burst_time"],
		cpuTotal:         usage.Usage * 1000,
		lastUpdate:       now,
	}

	deltaHierarchyWaitSum = stat.hierarchyWaitSum - cpu.hierarchyWaitSum
	if deltaHierarchyWaitSum <= 0 {
		deltaThrottledSum = 0
		deltaHierarchyWaitSum = 0
		deltaInnerWaitSum = 0
		deltaExterWaitSum = 0
	} else {
		deltaThrottledSum = stat.throttledTime - cpu.throttledTime
		deltaInnerWaitSum = stat.innerWaitSum - cpu.innerWaitSum

		if deltaHierarchyWaitSum < deltaThrottledSum+deltaInnerWaitSum {
			deltaHierarchyWaitSum = deltaThrottledSum + deltaInnerWaitSum
		}

		deltaExterWaitSum = deltaHierarchyWaitSum - deltaThrottledSum - deltaInnerWaitSum
	}

	deltaWaitRunSum := deltaHierarchyWaitSum + stat.cpuTotal - cpu.cpuTotal
	if deltaWaitRunSum == 0 {
		stat.waitrateHierarchy = 0
		stat.waitrateInner = 0
		stat.waitrateExter = 0
		stat.waitrateThrottled = 0
	} else {
		stat.waitrateHierarchy = float64(deltaHierarchyWaitSum) * 100 / float64(deltaWaitRunSum)
		stat.waitrateInner = float64(deltaInnerWaitSum) * 100 / float64(deltaWaitRunSum)
		stat.waitrateExter = float64(deltaExterWaitSum) * 100 / float64(deltaWaitRunSum)
		stat.waitrateThrottled = float64(deltaThrottledSum) * 100 / float64(deltaWaitRunSum)
	}

	*cpu = stat
	return nil
}

func (c *cpuStatCollector) Update() ([]*metric.Data, error) {
	metrics := []*metric.Data{}

	containers, err := pod.ContainersByType(pod.ContainerTypeNormal | pod.ContainerTypeSidecar)
	if err != nil {
		return nil, err
	}

	for _, container := range containers {
		containerMetric := container.LifeResouces("collector_cpu_stat").(*cpuStat)
		if err := c.cpuMetricUpdate(containerMetric, container); err != nil {
			log.Infof("failed to update cpu info of %s, %v", container, err)
			continue
		}

		metrics = append(metrics, metric.NewContainerGaugeData(container, "wait_rate", containerMetric.waitrateHierarchy, "wait rate for containers", nil),
			metric.NewContainerGaugeData(container, "inner_wait_rate", containerMetric.waitrateInner, "inner wait rate for container", nil),
			metric.NewContainerGaugeData(container, "exter_wait_rate", containerMetric.waitrateExter, "exter wait rate for container", nil),
			metric.NewContainerGaugeData(container, "throttle_wait_rate", containerMetric.waitrateThrottled, "throttle wait rate for container", nil),
			metric.NewContainerGaugeData(container, "nr_throttled", float64(containerMetric.nrThrottled), "throttle nr for container", nil),
			metric.NewContainerGaugeData(container, "nr_bursts", float64(containerMetric.nrBursts), "burst nr for container", nil),
			metric.NewContainerGaugeData(container, "burst_time", float64(containerMetric.burstTime), "burst time for container", nil),
		)
	}

	return metrics, nil
}
