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
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"

	"huatuo-bamai/internal/cgroups"
	"huatuo-bamai/internal/cgroups/stats"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/tracing"
	"huatuo-bamai/internal/utils/cpuutil"
	"huatuo-bamai/pkg/metric"
)

type cpuUtilStat struct {
	lastUsage     stats.CpuUsage
	lastTimestamp time.Time
	capacity      stats.CpuCapacity
}

type cpuUtilSample struct {
	total float64
	sys   float64
	usr   float64
}

type cpuUtilCollector struct {
	cgroup         cgroups.Cgroup
	cpuDataCache   cpuUtilStat
	mutex          sync.Mutex
	now            func() time.Time
	readOnlineCPUs func() (string, error)
}

func init() {
	tracing.RegisterEventTracing("cpu_util", newCpuCollector)
	_ = pod.RegisterContainerLifeResources("collector_cpu_util", reflect.TypeOf(&cpuUtilStat{}))
}

func readSystemOnlineCPUs() (string, error) {
	data, err := os.ReadFile(cpuutil.SystemCPUOnlinePath)
	if err != nil {
		return "", fmt.Errorf("read online CPUs: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

func hostCPUCapacity(onlineCPUs string) (stats.CpuCapacity, error) {
	numCores, err := cpuutil.ParseCPUListCount(onlineCPUs)
	if err != nil {
		return stats.CpuCapacity{}, fmt.Errorf("parse online CPUs: %w", err)
	}
	if numCores == 0 {
		return stats.CpuCapacity{}, errors.New("no online CPU")
	}
	return stats.CpuCapacity{
		Cores:    float64(numCores),
		ConfigID: sha256.Sum256([]byte(onlineCPUs)),
	}, nil
}

func newCpuCollector() (*tracing.EventTracingAttr, error) {
	cgroup, err := cgroups.NewManager()
	if err != nil {
		return nil, err
	}

	onlineCPUs, err := readSystemOnlineCPUs()
	if err != nil {
		return nil, err
	}
	if _, err := hostCPUCapacity(onlineCPUs); err != nil {
		return nil, err
	}

	return &tracing.EventTracingAttr{
		TracingData: &cpuUtilCollector{
			cgroup:         cgroup,
			now:            time.Now,
			readOnlineCPUs: readSystemOnlineCPUs,
		},
		Flag: tracing.FlagMetric,
	}, nil
}

func (c *cpuUtilCollector) sampleTime() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (cache *cpuUtilStat) update(usage stats.CpuUsage, capacity stats.CpuCapacity, now time.Time) (cpuUtilSample, bool) {
	next := cpuUtilStat{lastUsage: usage, lastTimestamp: now, capacity: capacity}

	// A delta needs two samples from the same cgroup and capacity configuration.
	// Reusing the old denominator across resize would invent utilization.
	if cache.lastTimestamp.IsZero() || cache.capacity != capacity ||
		now.Before(cache.lastTimestamp) ||
		usage.Usage < cache.lastUsage.Usage ||
		usage.User < cache.lastUsage.User ||
		usage.System < cache.lastUsage.System {
		*cache = next
		return cpuUtilSample{}, false
	}

	elapsed := now.Sub(cache.lastTimestamp)
	if elapsed < time.Second {
		return cpuUtilSample{}, false
	}

	denominator := capacity.Cores * float64(elapsed.Microseconds())
	sample := cpuUtilSample{
		total: float64(usage.Usage-cache.lastUsage.Usage) * 100 / denominator,
		usr:   float64(usage.User-cache.lastUsage.User) * 100 / denominator,
		sys:   float64(usage.System-cache.lastUsage.System) * 100 / denominator,
	}
	// Quota is a bandwidth limit, not an instantaneous ceiling: CFS burst
	// allowance and period boundaries can legitimately exceed 100%.
	*cache = next
	return sample, true
}

func (c *cpuUtilCollector) readCapacity(path, onlineCPUs string) (stats.CpuCapacity, error) {
	capacity, err := c.cgroup.CpuCapacity(path, onlineCPUs)
	if err != nil {
		return stats.CpuCapacity{}, err
	}
	if capacity == nil {
		return stats.CpuCapacity{}, errors.New("CPU capacity is unavailable")
	}
	if capacity.Cores <= 0 || math.IsNaN(capacity.Cores) || math.IsInf(capacity.Cores, 0) {
		return stats.CpuCapacity{}, fmt.Errorf("CPU capacity must be positive and finite, got %v", capacity.Cores)
	}
	return *capacity, nil
}

func (c *cpuUtilCollector) updateContainerDataCache(cache *cpuUtilStat, container *pod.Container, onlineCPUs string) ([]*metric.Data, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	before, err := c.readCapacity(container.CgroupPath, onlineCPUs)
	if err != nil {
		*cache = cpuUtilStat{}
		return nil, fmt.Errorf("read CPU capacity before usage: %w", err)
	}
	metrics := make([]*metric.Data, 1, 4)
	metrics[0] = metric.NewContainerGaugeData(container, "cores", before.Cores, "cpu core number for the containers", nil)

	usage, err := c.cgroup.CpuUsage(container.CgroupPath)
	if err != nil || usage == nil {
		*cache = cpuUtilStat{}
		if err == nil {
			err = errors.New("CPU usage is unavailable")
		}
		return metrics, fmt.Errorf("read CPU usage: %w", err)
	}
	sampledAt := c.sampleTime()

	after, err := c.readCapacity(container.CgroupPath, onlineCPUs)
	if err != nil {
		*cache = cpuUtilStat{}
		return metrics, fmt.Errorf("read CPU capacity after usage: %w", err)
	}
	metrics[0].Value = after.Cores
	if before != after {
		// The usage read cannot be assigned to either configuration reliably.
		*cache = cpuUtilStat{}
		return metrics, nil
	}

	sample, available := cache.update(*usage, after, sampledAt)
	if available {
		metrics = append(metrics,
			metric.NewContainerGaugeData(container, "usr", sample.usr, "cpu usr for the containers", nil),
			metric.NewContainerGaugeData(container, "sys", sample.sys, "cpu sys for the containers", nil),
			metric.NewContainerGaugeData(container, "total", sample.total, "cpu total for the containers", nil),
		)
	}
	return metrics, nil
}

func (c *cpuUtilCollector) updateHostDataCache(capacity stats.CpuCapacity) ([]*metric.Data, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	usage, err := c.cgroup.CpuUsage("")
	if err != nil || usage == nil {
		c.cpuDataCache = cpuUtilStat{}
		if err == nil {
			err = errors.New("CPU usage is unavailable")
		}
		return nil, err
	}
	sample, available := c.cpuDataCache.update(*usage, capacity, c.sampleTime())
	if !available {
		return nil, nil
	}

	return []*metric.Data{
		metric.NewGaugeData("usr", sample.usr, "cpu usr for the host", nil),
		metric.NewGaugeData("sys", sample.sys, "cpu sys for the host", nil),
		metric.NewGaugeData("total", sample.total, "cpu total for the host", nil),
	}, nil
}

func (c *cpuUtilCollector) Update() ([]*metric.Data, error) {
	containers, err := pod.ContainersByType(pod.ContainerTypeNormal | pod.ContainerTypeSidecar)
	if err != nil {
		return nil, err
	}
	return c.updateMetrics(containers)
}

func (c *cpuUtilCollector) updateMetrics(containers map[string]*pod.Container) ([]*metric.Data, error) {
	readOnline := c.readOnlineCPUs
	if readOnline == nil {
		readOnline = readSystemOnlineCPUs
	}
	onlineCPUs, err := readOnline()
	var hostCapacity stats.CpuCapacity
	if err == nil {
		onlineCPUs = strings.TrimSpace(onlineCPUs)
		hostCapacity, err = hostCPUCapacity(onlineCPUs)
	}
	if err != nil {
		// No sample may bridge a scrape whose available CPU set is unknown.
		c.mutex.Lock()
		c.cpuDataCache = cpuUtilStat{}
		for _, container := range containers {
			if cache, ok := container.LifeResources("collector_cpu_util").(*cpuUtilStat); ok && cache != nil {
				*cache = cpuUtilStat{}
			}
		}
		c.mutex.Unlock()
		return nil, err
	}

	metrics := make([]*metric.Data, 0, len(containers)*4+3)
	for _, container := range containers {
		dataCache, ok := container.LifeResources("collector_cpu_util").(*cpuUtilStat)
		if !ok || dataCache == nil {
			log.Warnf("cpu cache: container=%s unavailable", container)
			continue
		}
		data, err := c.updateContainerDataCache(dataCache, container, onlineCPUs)
		metrics = append(metrics, data...)
		if err != nil {
			log.Infof("cpu utilization: container=%s err=%v", container, err)
		}
	}

	more, err := c.updateHostDataCache(hostCapacity)
	if err != nil {
		log.Warnf("host cpu usage: %v", err)
	}
	return append(metrics, more...), nil
}
