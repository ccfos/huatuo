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

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/stats"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/internal/utils/cpuutil"
	"github.com/ccfos/huatuo/pkg/metric"
)

type cpuUtilStat struct {
	lastUsage     stats.CpuUsage
	lastTimestamp time.Time
	totalUtil     float64
	sysUtil       float64
	usrUtil       float64
	// utilValid reports whether totalUtil/usrUtil/sysUtil describe the interval
	// that ended at lastTimestamp. It is false until a second sample has been
	// observed, so the first scrape after a baseline (or after a re-baseline)
	// does not publish a lifetime average as if it were an interval rate.
	utilValid bool
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
	var cgroupPath string

	c.mutex.Lock()
	defer c.mutex.Unlock()

	now := time.Now()

	// A zero lastTimestamp means "no baseline yet", not "sampled in year 1":
	// now.Sub(time.Time{}) saturates at the maximum Duration (~292 years), so the
	// interval check below cannot detect the first sample on its own.
	firstSample := cache.lastTimestamp.IsZero()
	if !firstSample && now.Sub(cache.lastTimestamp) < time.Second {
		return nil
	}

	if container != nil {
		cgroupPath = container.CgroupPath
	}

	stat, err := c.cgroup.CpuUsage(cgroupPath)
	if err != nil {
		return err
	}

	// The counters are lifetime totals. On the first sample there is no previous
	// observation to subtract, so record the baseline and publish nothing. The
	// alternative is to divide a whole-lifetime total by the whole-lifetime
	// interval that a zero timestamp implies and report it as an interval rate.
	if firstSample {
		cache.lastUsage = *stat
		cache.lastTimestamp = now
		cache.utilValid = false
		return nil
	}

	totalUtil, usrUtil, sysUtil, ok := computeCPUUtil(cache.lastUsage, *stat, now.Sub(cache.lastTimestamp), numCores)
	if !ok {
		// A counter that ran backwards, or that advanced faster than the elapsed
		// wall clock and the core count can deliver, cannot be normalized: the
		// cgroup was recreated or the clock stepped. Re-baseline, publish nothing.
		cache.lastUsage = *stat
		cache.lastTimestamp = now
		cache.utilValid = false
		return nil
	}

	cache.lastUsage = *stat
	cache.lastTimestamp = now
	cache.totalUtil = totalUtil
	cache.usrUtil = usrUtil
	cache.sysUtil = sysUtil
	cache.utilValid = true
	return nil
}

// computeCPUUtil normalizes the CPU counter delta between two consecutive
// samples into a percentage of the capacity the workload could have used over
// the elapsed interval.
//
// It reports ok=false, rather than a number, whenever the delta cannot be
// normalized:
//
//   - a non-positive interval or core count, which leaves nothing to divide by;
//   - a counter that moved backwards, which is a reset rather than usage;
//   - a delta larger than the interval and the core count could physically
//     deliver, which is the signature of a stale or missing baseline.
func computeCPUUtil(prev, curr stats.CpuUsage, elapsed time.Duration, numCores float64) (total, usr, sys float64, ok bool) {
	if elapsed <= 0 || numCores <= 0 {
		return 0, 0, 0, false
	}

	// Usage, User, and System should increase monotonically. Checking before the
	// subtraction also keeps an unexpected reset from underflowing uint64.
	if curr.Usage < prev.Usage || curr.User < prev.User || curr.System < prev.System {
		return 0, 0, 0, false
	}

	capacity := numCores * float64(elapsed.Microseconds())
	if capacity <= 0 {
		return 0, 0, 0, false
	}

	deltaTotal := curr.Usage - prev.Usage
	deltaUsr := curr.User - prev.User
	deltaSys := curr.System - prev.System

	if float64(deltaTotal) > capacity || float64(deltaUsr+deltaSys) > capacity {
		return 0, 0, 0, false
	}

	return float64(deltaTotal) * 100 / capacity,
		float64(deltaUsr) * 100 / capacity,
		float64(deltaSys) * 100 / capacity,
		true
}

func (c *cpuUtilCollector) updateHostDataCache() ([]*metric.Data, error) {
	if err := c.updateDataCache(&c.cpuDataCache, nil, c.numCores); err != nil {
		return nil, err
	}

	if !c.cpuDataCache.utilValid {
		return nil, nil
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
		)

		// Without a baseline there is no rate to report. The core count is still
		// meaningful, but publishing usr/sys/total here would export a lifetime
		// average as though it were the current scrape interval's rate.
		if dataCache.utilValid {
			metrics = append(
				metrics,
				metric.NewContainerGaugeData(container, "usr", dataCache.usrUtil, "cpu usr for the containers", nil),
				metric.NewContainerGaugeData(container, "sys", dataCache.sysUtil, "cpu sys for the containers", nil),
				metric.NewContainerGaugeData(container, "total", dataCache.totalUtil, "cpu total for the containers", nil),
			)
		}
	}

	more, err := c.updateHostDataCache()
	if err != nil {
		log.Warnf("host cpu usage: %v", err)
	}

	return append(metrics, more...), nil
}
