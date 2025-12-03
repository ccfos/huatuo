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
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

type memEventsCollector struct {
	cgroup cgroups.Cgroup
}

func init() {
	tracing.RegisterEventTracing("memory_events", newMemEvents)
}

func newMemEvents() (*tracing.EventTracingAttr, error) {
	cgroup, _ := cgroups.NewCgroupManager()

	return &tracing.EventTracingAttr{
		TracingData: &memEventsCollector{
			cgroup: cgroup,
		}, Flag: tracing.FlagMetric,
	}, nil
}

func (c *memEventsCollector) Update() ([]*metric.Data, error) {
	filter := newFieldFilter(conf.Get().MetricCollector.MemoryEvents.Excluded,
		conf.Get().MetricCollector.MemoryEvents.Included)

	containers, err := pod.NormalContainers()
	if err != nil {
		return nil, fmt.Errorf("get normal container: %w", err)
	}

	metrics := []*metric.Data{}
	for _, container := range containers {
		raw, err := c.cgroup.MemoryEventRaw(container.CgroupSuffix)
		if err != nil {
			return nil, err
		}

		for key, value := range raw {
			if filter.ignored(key) {
				continue
			}

			metrics = append(metrics,
				metric.NewContainerGaugeData(container, key, float64(value), fmt.Sprintf("memory events %s", key), nil))
		}
	}

	return metrics, nil
}
