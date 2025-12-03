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
	"huatuo-bamai/internal/conf"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

type memStatCollector struct {
	cgroup cgroups.Cgroup
}

func init() {
	tracing.RegisterEventTracing("memory_stat", newMemStat)
}

func newMemStat() (*tracing.EventTracingAttr, error) {
	cgroup, err := cgroups.NewCgroupManager()
	if err != nil {
		return nil, err
	}

	return &tracing.EventTracingAttr{
		TracingData: &memStatCollector{
			cgroup: cgroup,
		},
		Flag: tracing.FlagMetric,
	}, nil
}

func (c *memStatCollector) Update() ([]*metric.Data, error) {
	filter := newFieldFilter(conf.Get().MetricCollector.MemoryStat.Excluded,
		conf.Get().MetricCollector.MemoryStat.Included)

	metrics := []*metric.Data{}
	containers, err := pod.NormalContainers()
	if err != nil {
		return nil, err
	}

	for _, container := range containers {
		raw, err := c.cgroup.MemoryStatRaw(container.CgroupSuffix)
		if err != nil {
			log.Infof("parse %s memory.stat %v", container.CgroupSuffix, err)
			continue
		}

		for m, v := range raw {
			if filter.ignored(m) {
				log.Debugf("Ignoring memory_stat metric: %s", m)
				continue
			}

			metrics = append(metrics, metric.NewContainerGaugeData(container, m, float64(v), fmt.Sprintf("memory stat %s", m), nil))
		}
	}

	return metrics, nil
}
