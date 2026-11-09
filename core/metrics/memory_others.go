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
	"os"
	"path/filepath"

	"github.com/ccfos/huatuo/internal/cgroups/paths"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/internal/utils/parseutil"
	"github.com/ccfos/huatuo/pkg/metric"
	"github.com/ccfos/huatuo/pkg/types"
)

type memOthersCollector struct{}

// didiMemcgMetricSpecs maps the memory cgroup extension files provided by the
// Didi Cloud custom kernel to the metrics exported by this collector.
// Mainline and distribution kernels do not expose these files.
var didiMemcgMetricSpecs = []struct {
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
	for _, root := range []string{paths.Path(subsystem.SubsystemMemory), paths.Path()} {
		for _, spec := range didiMemcgMetricSpecs {
			if _, err := os.Stat(filepath.Join(root, spec.path)); err == nil {
				return true
			}
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

func parseValueWithKey(directory, cgroupFile, key string) (uint64, error) {
	filePath := filepath.Join(directory, cgroupFile)
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
	workloads, err := pod.Pods()
	if err != nil {
		return nil, fmt.Errorf("get workloads: %w", err)
	}

	metrics := []*metric.Data{}

	for _, workload := range workloads {
		if !isMemoryWorkload(workload) {
			continue
		}
		directory, err := workload.Cgroups.Directory("memory")
		if err != nil {
			continue
		}
		for _, spec := range didiMemcgMetricSpecs {
			value, err := parseValueWithKey(directory, spec.path, spec.key)
			if err != nil {
				// FIXME: os maynot support this metric
				continue
			}

			metrics = append(metrics,
				metric.NewPodGaugeData(workload, spec.name, float64(value), fmt.Sprintf("memory cgroup %s", spec.name), nil))
		}
	}

	return metrics, nil
}
