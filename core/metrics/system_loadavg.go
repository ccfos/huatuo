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
	"huatuo-bamai/internal/cgroups"
	"huatuo-bamai/internal/cgroups/paths"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"

	"github.com/google/cadvisor/utils/cpuload/netlink"
)

type loadavgCollector struct{}

func init() {
	tracing.RegisterEventTracing("loadavg", newLoadavg)
}

// newLoadavg returns a new Collector exposing load average stats.
func newLoadavg() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &loadavgCollector{},
		Flag:        tracing.FlagMetric,
	}, nil
}

// Load average of last 1, 5, 15 minutes.
// See linux kernel Documentation/filesystems/proc.rst
func nodeLoadAvg() ([]*metric.Data, error) {
	fs, err := procfs.NewDefaultFS()
	if err != nil {
		return nil, err
	}

	load, err := fs.LoadAvg()
	if err != nil {
		return nil, err
	}

	return []*metric.Data{
		metric.NewGaugeData("load1", load.Load1, "system load average, 1 minute", nil),
		metric.NewGaugeData("load5", load.Load5, "system load average, 5 minutes", nil),
		metric.NewGaugeData("load15", load.Load15, "system load average, 15 minutes", nil),
	}, nil
}

func containerLoadavg() ([]*metric.Data, error) {
	n, err := netlink.New()
	if err != nil {
		return nil, err
	}
	defer n.Stop()

	containers, err := pod.ContainersByType(pod.ContainerTypeNormal | pod.ContainerTypeSidecar)
	if err != nil {
		return nil, err
	}

	loadavgs := []*metric.Data{}
	for _, container := range containers {
		stats, err := n.GetCpuLoad(container.Hostname, paths.Path("cpu", container.CgroupPath))
		if err != nil {
			continue
		}

		loadavgs = append(loadavgs,
			metric.NewContainerGaugeData(container,
				"nr_running", float64(stats.NrRunning), "nr_running of container", nil),
			metric.NewContainerGaugeData(container,
				"nr_uninterruptible", float64(stats.NrUninterruptible), "nr_uninterruptible of container", nil))
	}

	return loadavgs, nil
}

func (c *loadavgCollector) Update() ([]*metric.Data, error) {
	var loadavgs []*metric.Data

	if cgroups.CgroupMode() == cgroups.Legacy {
		// continue for node loadavg if err
		if containersLoads, err := containerLoadavg(); err == nil {
			loadavgs = append(loadavgs, containersLoads...)
		}
	}

	data, err := nodeLoadAvg()
	if err != nil {
		return loadavgs, err
	}

	return append(loadavgs, data...), nil
}
