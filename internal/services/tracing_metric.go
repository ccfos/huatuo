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

package services

import (
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

type tracingHitCollector struct{}

func init() {
	tracing.RegisterEventTracing("tracing_status", newTracingHit)
}

func newTracingHit() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &tracingHitCollector{},
		Flag:        tracing.FlagMetric,
	}, nil
}

func (trace *tracingHitCollector) Update() ([]*metric.Data, error) {
	var runningTracers int
	hitMetric := make([]*metric.Data, 0)

	for _, info := range instance.tracingManager.Dump() {
		hitMetric = append(hitMetric, metric.NewGaugeData("hitcount", float64(info.HitCount),
			"tracing hit count", map[string]string{"tracing": info.Name}))
		if info.Running {
			runningTracers++
		}
	}

	// huatuo-bamai.running_tracer.cnt
	hitMetric = append(hitMetric, metric.NewGaugeData("running", float64(runningTracers), "running tracing number", nil))
	return hitMetric, nil
}
