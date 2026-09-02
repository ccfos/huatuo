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
	"fmt"

	"huatuo-bamai/internal/cgroups"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/matcher"
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
	cgroup, err := cgroups.NewManager()
	if err != nil {
		return nil, fmt.Errorf("memory events: init cgroup manager: %w", err)
	}

	return &tracing.EventTracingAttr{
		TracingData: &memEventsCollector{
			cgroup: cgroup,
		}, Flag: tracing.FlagMetric,
	}, nil
}

func (c *memEventsCollector) Update() ([]*metric.Data, error) {
	containers, err := pod.NormalContainers()
	if err != nil {
		return nil, fmt.Errorf("get normal container: %w", err)
	}
	return c.collect(containers)
}

func (c *memEventsCollector) collect(containers map[string]*pod.Container) ([]*metric.Data, error) {
	cfg := configSnapshot()
	f, err := matcher.NewValueMatcher(cfg.MemoryEvents.Included, cfg.MemoryEvents.Excluded)
	if err != nil {
		return nil, fmt.Errorf("memory events filter: %w", err)
	}

	metrics := []*metric.Data{}
	for _, container := range containers {
		raw, err := c.cgroup.MemoryEventRaw(container.CgroupPath)
		if err != nil {
			// Container state can disappear after discovery without invalidating
			// metrics from targets that are still alive.
			log.Errorf("memory events for container %q (cgroup %q): %v", container.Name, container.CgroupPath, err)
			continue
		}

		for key, value := range raw {
			if !f.Match(key) {
				continue
			}

			metrics = append(metrics,
				metric.NewContainerGaugeData(container, key, float64(value), fmt.Sprintf("memory events %s", key), nil))
		}
	}

	return metrics, nil
}
