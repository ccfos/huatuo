// Copyright 2026 The HuaTuo Authors
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
	"io/fs"

	"huatuo-bamai/internal/cgroups/paths"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
	"huatuo-bamai/pkg/types"

	procfslib "github.com/prometheus/procfs"
)

// psiResources are the pressure-stall resources exposed by the kernel.
var psiResources = []string{"cpu", "memory", "io"}

// psiMicrosPerSecond converts the kernel's PSI total, which counts
// microseconds of stall time, into seconds.
const psiMicrosPerSecond = 1e6

type psiCollector struct{}

func init() {
	tracing.RegisterEventTracing("psi", newPSI)
}

func newPSI() (*tracing.EventTracingAttr, error) {
	// Pressure files require CONFIG_PSI (kernel 4.20+; per-cgroup files
	// exist only on cgroup v2).
	if err := procfs.RequireFile("pressure", "cpu"); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, types.ErrNotSupported
		}

		return nil, fmt.Errorf("check pressure statistics support: %w", err)
	}

	return &tracing.EventTracingAttr{
		TracingData: &psiCollector{},
		Flag:        tracing.FlagMetric,
	}, nil
}

// Update exports host and per-container pressure stall information.
// Per-container PSI exists only on cgroup v2; containers whose pressure
// files are missing or unreadable are skipped.
func (c *psiCollector) Update() ([]*metric.Data, error) {
	data, err := c.hostData()
	if err != nil {
		return nil, err
	}

	containerData, err := c.containerData()
	if err != nil {
		return nil, err
	}

	return append(data, containerData...), nil
}

func (c *psiCollector) hostData() ([]*metric.Data, error) {
	data := []*metric.Data{}
	for _, resource := range psiResources {
		stats, err := procfs.PSIStatsFromFile(procfs.Path("pressure", resource))
		if err != nil {
			return nil, fmt.Errorf("read host pressure for %s: %w", resource, err)
		}
		data = append(data, psiData(nil, resource, stats)...)
	}
	return data, nil
}

func (c *psiCollector) containerData() ([]*metric.Data, error) {
	containers, err := pod.NormalContainers()
	if err != nil {
		return nil, fmt.Errorf("get normal container: %w", err)
	}

	data := []*metric.Data{}
	for _, container := range containers {
		for _, resource := range psiResources {
			stats, err := procfs.PSIStatsFromFile(
				paths.Path(container.CgroupPath, resource+".pressure"))
			if err != nil {
				// v1 cgroups and containers with the controller disabled
				// have no pressure files.
				log.Debugf("psi: skip container %v resource %s: %v", container, resource, err)
				continue
			}
			data = append(data, psiData(container, resource, stats)...)
		}
	}

	return data, nil
}

// psiData converts one parsed pressure file into metric series. A nil
// container marks host-level data.
func psiData(container *pod.Container, resource string, stats procfslib.PSIStats) []*metric.Data {
	data := []*metric.Data{}
	for _, kind := range []struct {
		name string
		line *procfslib.PSILine
	}{
		{"some", stats.Some},
		{"full", stats.Full},
	} {
		if kind.line == nil {
			continue
		}

		prefix := fmt.Sprintf("%s_%s", resource, kind.name)
		desc := fmt.Sprintf("%s tasks stalled", kind.name)
		values := []struct {
			name    string
			value   float64
			counter bool
		}{
			{prefix + "_avg10", kind.line.Avg10, false},
			{prefix + "_avg60", kind.line.Avg60, false},
			{prefix + "_avg300", kind.line.Avg300, false},
			{prefix + "_seconds_total", float64(kind.line.Total) / psiMicrosPerSecond, true},
		}

		for _, v := range values {
			help := fmt.Sprintf("%s: PSI %s", desc, v.name)
			if container != nil {
				if v.counter {
					data = append(data, metric.NewContainerCounterData(container, v.name, v.value, help, nil))
				} else {
					data = append(data, metric.NewContainerGaugeData(container, v.name, v.value, help, nil))
				}
				continue
			}
			if v.counter {
				data = append(data, metric.NewCounterData(v.name, v.value, help, nil))
			} else {
				data = append(data, metric.NewGaugeData(v.name, v.value, help, nil))
			}
		}
	}

	return data
}
