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
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"huatuo-bamai/internal/cgroups"
	"huatuo-bamai/internal/cgroups/stats"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/utils/cpuutil"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

type cpuUtilStat struct {
	lastUsage     stats.CpuUsage
	lastTimestamp time.Time
	totalUtil     float64
	sysUtil       float64
	usrUtil       float64
}

type cpuUtilCollector struct {
	cgroup       cgroups.Cgroup
	numCores     float64
	cpuDataCache cpuUtilStat
	mutex        sync.Mutex
}

func init() {
	tracing.RegisterEventTracing("cpu_util", newCpuCollector)
	_ = pod.RegisterContainerLifeResources("collector_cpu_util", reflect.TypeOf(&cpuUtilStat{}))
}

func newCpuCollector() (*tracing.EventTracingAttr, error) {
	cgroup, err := cgroups.NewManager()
	if err != nil {
		return nil, err
	}

	numCores, err := cpuutil.ParseOnlineCores(cpuutil.SystemCPUOnlinePath)
	if err != nil {
		return nil, fmt.Errorf("read online cpu: %w", err)
	}
	if numCores == 0 {
		return nil, errors.New("no online cpu")
	}

	return &tracing.EventTracingAttr{
		TracingData: &cpuUtilCollector{
			numCores: float64(numCores),
			cgroup:   cgroup,
		},
		Flag: tracing.FlagMetric,
	}, nil
}

func (c *cpuUtilCollector) updateDataCache(cache *cpuUtilStat, container *pod.Container, numCores float64) error {
	var (
		usrUtil    float64
		sysUtil    float64
		totalUtil  float64
		cgroupPath string
	)

	c.mutex.Lock()
	defer c.mutex.Unlock()

	now := time.Now()
	if now.Sub(cache.lastTimestamp).Nanoseconds() < 1000000000 {
		return nil
	}

	if container != nil {
		cgroupPath = container.CgroupPath
	}

	stat, err := c.cgroup.CpuUsage(cgroupPath)
	if err != nil {
		return err
	}

	// Usage, User, and System should increase monotonically. This defensive
	// check prevents an unexpected reset from causing uint64 underflow.
	if stat.Usage < cache.lastUsage.Usage ||
		stat.User < cache.lastUsage.User ||
		stat.System < cache.lastUsage.System {
		cache.lastUsage = *stat
		cache.lastTimestamp = now
		return nil
	}

	deltaTotalTime := stat.Usage - cache.lastUsage.Usage
	deltaUsrTime := stat.User - cache.lastUsage.User
	deltaSysTime := stat.System - cache.lastUsage.System
	deltaRealWorldTime := numCores * float64(now.Sub(cache.lastTimestamp).Microseconds())

	if (float64(deltaTotalTime) > deltaRealWorldTime) || (float64(deltaUsrTime+deltaSysTime) > deltaRealWorldTime) {
		cache.lastUsage = *stat
		cache.lastTimestamp = now
		return nil
	}

	totalUtil = float64(deltaTotalTime) * 100 / deltaRealWorldTime
	usrUtil = float64(deltaUsrTime) * 100 / deltaRealWorldTime
	sysUtil = float64(deltaSysTime) * 100 / deltaRealWorldTime

	cache.lastUsage = *stat
	cache.totalUtil = totalUtil
	cache.usrUtil = usrUtil
	cache.sysUtil = sysUtil
	cache.lastTimestamp = now
	return nil
}

func (c *cpuUtilCollector) updateHostDataCache() ([]*metric.Data, error) {
	if err := c.updateDataCache(&c.cpuDataCache, nil, c.numCores); err != nil {
		return nil, err
	}

	return []*metric.Data{
		metric.NewGaugeData("usr", c.cpuDataCache.usrUtil, "cpu usr for the host", nil),
		metric.NewGaugeData("sys", c.cpuDataCache.sysUtil, "cpu sys for the host", nil),
		metric.NewGaugeData("total", c.cpuDataCache.totalUtil, "cpu total for the host", nil),
	}, nil
}

func (c *cpuUtilCollector) Update() ([]*metric.Data, error) {
	metrics := []*metric.Data{}

	containers, err := pod.ContainersByType(pod.ContainerTypeNormal | pod.ContainerTypeSidecar)
	if err != nil {
		return nil, err
	}

	for _, container := range containers {
		cpuQuota, err := c.cgroup.CpuQuotaAndPeriod(container.CgroupPath)
		if err != nil {
			log.Infof("cpu quota: container=%s err=%v", container, err)
			continue
		}

		numCores, err := cpuutil.BoundCores(
			cpuQuota.Quota, cpuQuota.Period,
			cpuQuota.EffectiveCPUCount, uint64(c.numCores),
		)
		if err != nil {
			log.Infof("cpu capacity: container=%s err=%v", container, err)
			continue
		}

		dataCache, ok := container.LifeResources("collector_cpu_util").(*cpuUtilStat)
		if !ok || dataCache == nil {
			log.Warnf("cpu cache: container=%s unavailable", container)
			continue
		}
		if err := c.updateDataCache(dataCache, container, numCores); err != nil {
			log.Infof("cpu usage: container=%s err=%v", container, err)
			continue
		}

		metrics = append(
			metrics,
			metric.NewContainerGaugeData(container, "cores", numCores, "cpu core number for the containers", nil),
			metric.NewContainerGaugeData(container, "usr", dataCache.usrUtil, "cpu usr for the containers", nil),
			metric.NewContainerGaugeData(container, "sys", dataCache.sysUtil, "cpu sys for the containers", nil),
			metric.NewContainerGaugeData(container, "total", dataCache.totalUtil, "cpu total for the containers", nil),
		)
	}

	more, err := c.updateHostDataCache()
	if err != nil {
		log.Warnf("host cpu usage: %v", err)
	}

	return append(metrics, more...), nil
}
