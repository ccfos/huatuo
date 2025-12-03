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

	"huatuo-bamai/internal/cgroups/paths"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/utils/parseutil"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

type memOthersCollector struct{}

func init() {
	// only for didicloud
	tracing.RegisterEventTracing("memory_others", newMemOthersCollector)
}

func newMemOthersCollector() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &memOthersCollector{},
		Flag:        tracing.FlagMetric,
	}, nil
}

func parseValueWithKey(cgroupPath, cgroupFile, key string) (uint64, error) {
	filePath := paths.Path("memory", cgroupPath, cgroupFile)
	if key == "" {
		return parseutil.ReadUint(filePath)
	}

	raw, err := parseutil.RawKV(filePath)
	if err != nil {
		return 0, err
	}

	return raw[key], nil
}

func (c *memOthersCollector) Update() ([]*metric.Data, error) {
	containers, err := pod.NormalContainers()
	if err != nil {
		return nil, fmt.Errorf("Can't get normal container: %w", err)
	}

	metrics := []*metric.Data{}

	for _, container := range containers {
		for _, t := range []struct {
			path string
			key  string
			name string
		}{
			{
				path: "memory.directstall_stat",
				key:  "directstall_time",
				name: "directstall_time",
			},
			{
				path: "memory.asynreclaim_stat",
				key:  "asyncreclaim_time",
				name: "asyncreclaim_time",
			},
			{
				path: "memory.local_direct_reclaim_time",
				key:  "",
				name: "local_direct_reclaim_time",
			},
		} {
			value, err := parseValueWithKey(container.CgroupSuffix, t.path, t.key)
			if err != nil {
				// FIXME: os maynot support this metric
				continue
			}

			metrics = append(metrics,
				metric.NewContainerGaugeData(container, t.name, float64(value), fmt.Sprintf("memory cgroup %s", t.name), nil))
		}
	}

	return metrics, nil
}
