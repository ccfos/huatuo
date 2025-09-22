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
	"slices"
	"time"

	"huatuo-bamai/internal/cgroups"
	"huatuo-bamai/internal/cgroups/stats"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"

	"github.com/prometheus/procfs"
)

type runtimeCollector struct {
	cgroup     cgroups.Cgroup
	usage      *stats.CpuUsage
	latestTime time.Time
}

var runTimePath string

func init() {
	tracing.RegisterEventTracing("runtime", newRuntimeCollector)
}

func newRuntimeCollector() (*tracing.EventTracingAttr, error) {
	mgr, _ := cgroups.NewCgroupManager()

	runTimePath = "huatuo-bamai"
	if cgroups.CgroupMode() == cgroups.Unified {
		runTimePath = "huatuo.slice/huatuo-bamai.slice/"
	}

	return &tracing.EventTracingAttr{
		Flag:        tracing.FlagMetric,
		TracingData: &runtimeCollector{cgroup: mgr},
	}, nil
}

func (c *runtimeCollector) Update() ([]*metric.Data, error) {
	return slices.Concat(c.memoryUsage(), c.cpuUsage()), nil
}

func (c *runtimeCollector) cpuUsage() []*metric.Data {
	usage, err := c.cgroup.CpuUsage(runTimePath)
	if err != nil {
		return nil
	}

	if c.usage == nil {
		c.usage = usage
		c.latestTime = time.Now()
		return nil
	}

	now := time.Now()
	updateInterval := uint64(now.Sub(c.latestTime).Microseconds())

	userPercentage := 100 * (usage.User - c.usage.User) / updateInterval
	sysPercentage := 100 * (usage.System - c.usage.System) / updateInterval
	usagePercentage := 100 * (usage.Usage - c.usage.Usage) / updateInterval

	// update the time and usage
	c.usage = usage
	c.latestTime = now

	return []*metric.Data{
		metric.NewGaugeData("cpu_user", float64(userPercentage), "user cpu", nil),
		metric.NewGaugeData("cpu_sys", float64(sysPercentage), "sys cpu", nil),
		metric.NewGaugeData("cpu_total", float64(usagePercentage), "total cpu", nil),
	}
}

func (c *runtimeCollector) memoryUsage() []*metric.Data {
	p, err := procfs.Self()
	if err != nil {
		return nil
	}

	status, err := p.NewStatus()
	if err != nil {
		return nil
	}

	return []*metric.Data{
		metric.NewGaugeData("memory_vss", float64(status.VmSize)/1024, "memory vss", nil),
		metric.NewGaugeData("memory_rss", float64(status.VmRSS)/1024, "memory rss", nil),
	}
}
