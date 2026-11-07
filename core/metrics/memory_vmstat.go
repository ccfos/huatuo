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
	"path/filepath"

	"github.com/ccfos/huatuo/internal/matcher"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/internal/utils/parseutil"
	"github.com/ccfos/huatuo/pkg/metric"
)

type memoryVmStat struct{}

func init() {
	tracing.RegisterEventTracing("memory_vmstat", newMemoryVmStat)
}

func newMemoryVmStat() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &memoryVmStat{},
		Flag:        tracing.FlagMetric,
	}, nil
}

func (c *memoryVmStat) Update() ([]*metric.Data, error) {
	workloads, err := c.workloadVmstat()
	if err != nil {
		return nil, err
	}

	host, err := c.hostVmstat()
	if err != nil {
		return workloads, nil
	}

	return append(workloads, host...), nil
}

func (c *memoryVmStat) workloadVmstat() ([]*metric.Data, error) {
	cfg := configSnapshot()
	// Keep legacy configuration names until workload filter settings are unified.
	f, err := matcher.NewValueMatcher(cfg.Vmstat.IncludedOnContainer, cfg.Vmstat.ExcludedOnContainer)
	if err != nil {
		return nil, fmt.Errorf("vmstat cgroup filter: %w", err)
	}

	workloads, err := pod.Pods()
	if err != nil {
		return nil, err
	}

	var metrics []*metric.Data
	for _, workload := range workloads {
		if !isMemoryWorkload(workload) {
			continue
		}
		directory, err := workload.Cgroups.Directory("memory")
		if err != nil {
			continue
		}
		raw, err := parseutil.RawKV(filepath.Join(directory, "memory.stat"))
		if err != nil {
			log.Infof("parse %s memory.stat %v", directory, err)
			continue
		}

		for m, v := range raw {
			if !f.Match(m) {
				log.Debugf("Ignoring the cgroup memory.stat: %s", m)
				continue
			}

			metrics = append(metrics, metric.NewPodGaugeData(workload, m, float64(v), fmt.Sprintf("cgroup memory.stat %s", m), nil))
		}
	}

	return metrics, nil
}

func (c *memoryVmStat) hostVmstat() ([]*metric.Data, error) {
	cfg := configSnapshot()
	f, err := matcher.NewValueMatcher(cfg.Vmstat.IncludedOnHost, cfg.Vmstat.ExcludedOnHost)
	if err != nil {
		return nil, fmt.Errorf("vmstat host filter: %w", err)
	}

	raw, err := parseutil.RawKV(procfs.Path("vmstat"))
	if err != nil {
		return nil, err
	}

	var metrics []*metric.Data
	for m, v := range raw {
		if !f.Match(m) {
			log.Debugf("Ignoring the host vmstat: %s", m)
			continue
		}

		metrics = append(metrics,
			metric.NewGaugeData(m, float64(v), fmt.Sprintf("/proc/vmstat %s", m), nil))
	}

	return metrics, nil
}
