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

package handlers

import (
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

var tracingStatusCollector = &tracingHitCollector{}

type tracingHitCollector struct {
	manager *tracing.Manager
}

func NewTracingHitCollector(manager *tracing.Manager) *tracingHitCollector {
	return &tracingHitCollector{manager: manager}
}

func init() {
	tracing.RegisterEventTracing("tracing_status", func() (*tracing.EventTracingAttr, error) {
		return &tracing.EventTracingAttr{
			TracingData: tracingStatusCollector,
			Flag:        tracing.FlagMetric,
		}, nil
	})
}

func SetTracingManager(manager *tracing.Manager) {
	tracingStatusCollector.manager = manager
}

func (c *tracingHitCollector) Update() ([]*metric.Data, error) {
	var runningTracers int

	if c.manager == nil {
		return nil, nil
	}

	snapshots := c.manager.Snapshots()
	metrics := make([]*metric.Data, 0, len(snapshots)+1)
	for _, snapshot := range snapshots {
		metrics = append(metrics, metric.NewGaugeData(
			"hitcount",
			float64(snapshot.RunCount),
			"tracing hit count",
			map[string]string{"tracing": snapshot.Name},
		))
		if snapshot.IsRunning {
			runningTracers++
		}
	}

	metrics = append(metrics, metric.NewGaugeData("running", float64(runningTracers), "running tracing number", nil))
	return metrics, nil
}
