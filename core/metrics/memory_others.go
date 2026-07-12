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
	"os"

	"huatuo-bamai/internal/cgroups/paths"
	"huatuo-bamai/internal/cgroups/subsystem"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/utils/parseutil"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
	"huatuo-bamai/pkg/types"
)

type memOthersCollector struct{}

// didiMemcgMetrics maps the memory cgroup extension files provided by the
// Didi Cloud custom kernel to the metrics exported by this collector.
// Mainline and distribution kernels do not expose these files.
var didiMemcgMetrics = []struct {
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
}

func init() {
	// only for didicloud
	tracing.RegisterEventTracing("memory_others", newMemOthersCollector)
}

// hasDidiMemcgInterfaces reports whether the running kernel exposes any of
// the Didi memcg extension files on the root memory cgroup.
func hasDidiMemcgInterfaces() bool {
	for _, t := range didiMemcgMetrics {
		if _, err := os.Stat(paths.Path(subsystem.SubsystemMemory, t.path)); err == nil {
			return true
		}
	}

	return false
}

func newMemOthersCollector() (*tracing.EventTracingAttr, error) {
	if !hasDidiMemcgInterfaces() {
		log.Infof("memory_others: kernel does not expose Didi memcg interfaces (e.g. memory.directstall_stat), disabling")
		return nil, types.ErrNotSupported
	}

	return &tracing.EventTracingAttr{
		TracingData: &memOthersCollector{},
		Flag:        tracing.FlagMetric,
	}, nil
}

func parseValueWithKey(cgroupPath, cgroupFile, key string) (uint64, error) {
	filePath := paths.Path(subsystem.SubsystemMemory, cgroupPath, cgroupFile)
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
		for _, t := range didiMemcgMetrics {
			value, err := parseValueWithKey(container.CgroupPath, t.path, t.key)
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
