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
	"reflect"

	"huatuo-bamai/internal/pod"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

type runqlatCollector struct {
	runqlatMetric []*metric.Data
}

func init() {
	_ = pod.RegisterContainerLifeResources("runqlat", reflect.TypeOf(&latencyBpfData{}))
	tracing.RegisterEventTracing("runqlat", newRunqlatCollector)
}

func newRunqlatCollector() (*tracing.EventTracingAttr, error) {
	collector := &runqlatCollector{
		runqlatMetric: []*metric.Data{
			metric.NewGaugeData("g_nlat_01", 0, "nlat_01 of host", nil),
			metric.NewGaugeData("g_nlat_02", 0, "nlat_02 of host", nil),
			metric.NewGaugeData("g_nlat_03", 0, "nlat_03 of host", nil),
			metric.NewGaugeData("g_nlat_04", 0, "nlat_04 of host", nil),
		},
	}

	return &tracing.EventTracingAttr{
		TracingData: collector,
		Interval:    10,
		Flag:        tracing.FlagTracing | tracing.FlagMetric,
	}, nil
}

func (c *runqlatCollector) Update() ([]*metric.Data, error) {
	runqlatMetric := []*metric.Data{}

	if !runqlatRunning {
		return nil, nil
	}

	containers, err := pod.ContainersByType(pod.ContainerTypeNormal)
	if err != nil {
		return nil, err
	}

	for _, container := range containers {
		metrics := container.LifeResouces("runqlat").(*latencyBpfData)

		runqlatMetric = append(runqlatMetric,
			metric.NewContainerGaugeData(container, "nlat_01", float64(metrics.NumLatency01), "nlat_01", nil),
			metric.NewContainerGaugeData(container, "nlat_02", float64(metrics.NumLatency02), "nlat_02", nil),
			metric.NewContainerGaugeData(container, "nlat_03", float64(metrics.NumLatency03), "nlat_03", nil),
			metric.NewContainerGaugeData(container, "nlat_04", float64(metrics.NumLatency04), "nlat_04", nil))
	}

	c.runqlatMetric[0].Value = float64(globalRunqlat.NumLatency01)
	c.runqlatMetric[1].Value = float64(globalRunqlat.NumLatency02)
	c.runqlatMetric[2].Value = float64(globalRunqlat.NumLatency03)
	c.runqlatMetric[3].Value = float64(globalRunqlat.NumLatency04)

	runqlatMetric = append(runqlatMetric, c.runqlatMetric...)

	return runqlatMetric, nil
}
