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
	"fmt"

	"huatuo-bamai/internal/cgroups"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/internal/utils/parseutil"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

type memoryVmStat struct {
	cgroup cgroups.Cgroup
}

func init() {
	tracing.RegisterEventTracing("memory_vmstat", newMemoryVmStat)
}

func newMemoryVmStat() (*tracing.EventTracingAttr, error) {
	cgroup, err := cgroups.NewManager()
	if err != nil {
		return nil, err
	}

	return &tracing.EventTracingAttr{
		TracingData: &memoryVmStat{
			cgroup: cgroup,
		},
		Flag: tracing.FlagMetric,
	}, nil
}

func (c *memoryVmStat) Update() ([]*metric.Data, error) {
	container, err := c.containerVmstat()
	if err != nil {
		return nil, err
	}

	host, err := c.hostVmstat()
	if err != nil {
		return container, nil
	}

	return append(container, host...), nil
}

func (c *memoryVmStat) containerVmstat() ([]*metric.Data, error) {
	filter := newFieldFilter(cfg.Vmstat.ExcludedOnContainer, cfg.Vmstat.IncludedOnContainer)

	containers, err := pod.NormalContainers()
	if err != nil {
		return nil, err
	}

	var metrics []*metric.Data
	for _, container := range containers {
		raw, err := c.cgroup.MemoryStatRaw(container.CgroupPath)
		if err != nil {
			log.Infof("parse %s memory.stat %v", container.CgroupPath, err)
			continue
		}

		for m, v := range raw {
			if filter.ignored(m) {
				log.Debugf("Ignoring the cgroup memory.stat: %s", m)
				continue
			}

			metrics = append(metrics, metric.NewContainerGaugeData(container, m, float64(v), fmt.Sprintf("cgroup memory.stat %s", m), nil))
		}
	}

	return metrics, nil
}

func (c *memoryVmStat) hostVmstat() ([]*metric.Data, error) {
	filter := newFieldFilter(cfg.Vmstat.ExcludedOnHost, cfg.Vmstat.IncludedOnHost)

	raw, err := parseutil.RawKV(procfs.Path("vmstat"))
	if err != nil {
		return nil, err
	}

	var metrics []*metric.Data
	for m, v := range raw {
		if filter.ignored(m) {
			log.Debugf("Ignoring the host vmstat: %s", m)
			continue
		}

		metrics = append(metrics,
			metric.NewGaugeData(m, float64(v), fmt.Sprintf("/proc/vmstat %s", m), nil))
	}

	return metrics, nil
}
