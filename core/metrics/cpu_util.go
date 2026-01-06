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
	"math"
	"reflect"
	"runtime"
	"sync"
	"time"

	"huatuo-bamai/internal/cgroups"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

type cpuMetric struct {
	lastUsrTime  uint64
	lastSysTime  uint64
	lastCPUTotal uint64
	lasTimestamp time.Time
	utilTotal    float64
	utilSys      float64
	utilUsr      float64
}

type cpuUtilCollector struct {
	cpuUtil []*metric.Data
	cgroup  cgroups.Cgroup

	// included struct for used in multi modules
	hostCPUCount  int64
	hostCPUMetric cpuMetric

	mutex sync.Mutex
}

func init() {
	tracing.RegisterEventTracing("cpu_util", newCPUUtil)
	_ = pod.RegisterContainerLifeResources("collector_cpu_util", reflect.TypeOf(&cpuMetric{}))
}

func newCPUUtil() (*tracing.EventTracingAttr, error) {
	cgroup, err := cgroups.NewCgroupManager()
	if err != nil {
		return nil, err
	}

	return &tracing.EventTracingAttr{
		TracingData: &cpuUtilCollector{
			cpuUtil: []*metric.Data{
				metric.NewGaugeData("usr", 0, "usr for container and host", nil),
				metric.NewGaugeData("sys", 0, "sys for container and host", nil),
				metric.NewGaugeData("total", 0, "total for container and host", nil),
			},
			hostCPUCount: int64(runtime.NumCPU()),
			cgroup:       cgroup,
		},
		Flag: tracing.FlagMetric,
	}, nil
}

func (c *cpuUtilCollector) cpuMetricUpdate(cpuMetric *cpuMetric, container *pod.Container, cpuCount int64) error {
	var (
		utilUsr    float64
		utilSys    float64
		utilTotal  float64
		cgroupPath string
	)

	c.mutex.Lock()
	defer c.mutex.Unlock()

	now := time.Now()
	if now.Sub(cpuMetric.lasTimestamp).Nanoseconds() < 1000000000 {
		return nil
	}

	if container != nil {
		cgroupPath = container.CgroupSuffix
	}

	stat, err := c.cgroup.CpuUsage(cgroupPath)
	if err != nil {
		return err
	}

	usageTotal := stat.Usage
	usageUsr := stat.User
	usageSys := stat.System

	// allow statistics 0
	deltaTotal := usageTotal - cpuMetric.lastCPUTotal
	deltaUsrTime := usageUsr - cpuMetric.lastUsrTime
	deltaSysTime := usageSys - cpuMetric.lastSysTime
	deltaUsageSum := float64(cpuCount) * float64(now.Sub(cpuMetric.lasTimestamp).Microseconds())

	if (float64(deltaTotal) > deltaUsageSum) || (float64(deltaUsrTime+deltaSysTime) > deltaUsageSum) {
		cpuMetric.lastUsrTime = usageUsr
		cpuMetric.lastSysTime = usageSys
		cpuMetric.lastCPUTotal = usageTotal
		cpuMetric.lasTimestamp = now

		return nil
	}

	utilTotal = float64(deltaTotal) * 100 / deltaUsageSum
	utilUsr = float64(deltaUsrTime) * 100 / deltaUsageSum
	utilSys = float64(deltaSysTime) * 100 / deltaUsageSum

	cpuMetric.lastUsrTime = usageUsr
	cpuMetric.lastSysTime = usageSys
	cpuMetric.lastCPUTotal = usageTotal
	cpuMetric.utilTotal = utilTotal
	cpuMetric.utilUsr = utilUsr
	cpuMetric.utilSys = utilSys
	cpuMetric.lasTimestamp = now
	return nil
}

func (c *cpuUtilCollector) hostMetricUpdate() error {
	if err := c.cpuMetricUpdate(&c.hostCPUMetric, nil, c.hostCPUCount); err != nil {
		return err
	}

	c.cpuUtil[0].Value = c.hostCPUMetric.utilUsr
	c.cpuUtil[1].Value = c.hostCPUMetric.utilSys
	c.cpuUtil[2].Value = c.hostCPUMetric.utilTotal
	return nil
}

func (c *cpuUtilCollector) Update() ([]*metric.Data, error) {
	metrics := []*metric.Data{}

	containers, err := pod.ContainersByType(pod.ContainerTypeNormal | pod.ContainerTypeSidecar)
	if err != nil {
		return nil, err
	}

	for _, container := range containers {
		cpuQuota, err := c.cgroup.CpuQuotaAndPeriod(container.CgroupSuffix)
		if err != nil {
			log.Infof("fetch container [%s] cpu quota and period: %v", container, err)
			continue
		}

		var cpuCores int64
		if cpuQuota.Quota == math.MaxUint64 {
			cpuCores = int64(runtime.NumCPU())
		} else {
			cpuCores = int64(cpuQuota.Quota / cpuQuota.Period)
		}

		if cpuCores <= 0 {
			continue
		}

		containerMetric := container.LifeResouces("collector_cpu_util").(*cpuMetric)
		if err := c.cpuMetricUpdate(containerMetric, container, cpuCores); err != nil {
			log.Infof("failed to update cpu info of %s, %v", container, err)
			continue
		}

		metrics = append(metrics, metric.NewContainerGaugeData(container, "count", float64(cpuCores), "cpu count for containers", nil),
			metric.NewContainerGaugeData(container, "usr", containerMetric.utilUsr, "usr for container and host", nil),
			metric.NewContainerGaugeData(container, "sys", containerMetric.utilSys, "sys for container and host", nil),
			metric.NewContainerGaugeData(container, "total", containerMetric.utilTotal, "total for container and host", nil))
	}

	if err := c.hostMetricUpdate(); err != nil {
		return nil, err
	}

	metrics = append(metrics, c.cpuUtil...)
	return metrics, nil
}
