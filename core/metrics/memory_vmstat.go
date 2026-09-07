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
	"errors"
	"fmt"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/matcher"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/internal/utils/parseutil"
	"github.com/ccfos/huatuo/pkg/metric"
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
	return c.update(pod.NormalContainers)
}

func (c *memoryVmStat) update(discover func() (map[string]*pod.Container, error)) ([]*metric.Data, error) {
	container, containerErr := c.containerVmstat(discover)
	if containerErr != nil {
		containerErr = fmt.Errorf("container vmstat: %w", containerErr)
	}

	host, hostErr := c.hostVmstat()
	if hostErr != nil {
		hostErr = fmt.Errorf("host vmstat: %w", hostErr)
	}

	return append(container, host...), errors.Join(containerErr, hostErr)
}

func (c *memoryVmStat) containerVmstat(discover func() (map[string]*pod.Container, error)) ([]*metric.Data, error) {
	cfg := configSnapshot()
	f, err := matcher.NewValueMatcher(cfg.Vmstat.IncludedOnContainer, cfg.Vmstat.ExcludedOnContainer)
	if err != nil {
		return nil, fmt.Errorf("vmstat container filter: %w", err)
	}

	containers, err := discover()
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
			if !f.Match(m) {
				log.Debugf("Ignoring the cgroup memory.stat: %s", m)
				continue
			}

			metrics = append(metrics, metric.NewContainerGaugeData(container, m, float64(v), fmt.Sprintf("cgroup memory.stat %s", m), nil))
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
