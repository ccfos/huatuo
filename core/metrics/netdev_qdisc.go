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

// ref: https://github.com/prometheus/node_exporter/tree/master/collector
//	- qdisc_linux.go

import (
	"fmt"

	"huatuo-bamai/internal/matcher"
	"huatuo-bamai/internal/qdisc"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

type qdiscCollector struct {
	get func() ([]qdisc.Info, error)
}

const metricsPerQdisc = 7

func init() {
	tracing.RegisterEventTracing("netdev_qdisc", newQdiscCollector)
}

func newQdiscCollector() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &qdiscCollector{get: qdisc.Get},
		Flag:        tracing.FlagMetric,
	}, nil
}

func (c *qdiscCollector) Update() ([]*metric.Data, error) {
	f, err := matcher.NewValueMatcher(cfg.Qdisc.DeviceIncluded, cfg.Qdisc.DeviceExcluded)
	if err != nil {
		return nil, fmt.Errorf("qdisc device filter: %w", err)
	}

	allQdisc, err := c.get()
	if err != nil {
		return nil, fmt.Errorf("get qdiscs: %w", err)
	}

	rootQdiscs := make([]*qdisc.Info, 0)
	for i := range allQdisc {
		q := &allQdisc[i]
		if !f.Match(q.IfaceName) || q.Kind == "noqueue" || q.Parent != 0 {
			continue
		}
		rootQdiscs = append(rootQdiscs, q)
	}

	metrics := make([]*metric.Data, 0, len(rootQdiscs)*metricsPerQdisc)
	for _, q := range rootQdiscs {
		tags := map[string]string{"device": q.IfaceName, "kind": q.Kind}
		metrics = append(
			metrics,
			metric.NewCounterData("bytes_total", float64(q.Bytes), "number of bytes sent.", tags),
			metric.NewCounterData("packets_total", float64(q.Packets), "number of packets sent.", tags),
			metric.NewCounterData("drops_total", float64(q.Drops), "number of packet drops.", tags),
			metric.NewCounterData("requeues_total", float64(q.Requeues), "number of packets dequeued, not transmitted, and requeued.", tags),
			metric.NewCounterData("overlimits_total", float64(q.Overlimits), "number of packet overlimits.", tags),
			metric.NewGaugeData("current_queue_length", float64(q.Qlen), "number of packets currently in queue to be sent.", tags),
			metric.NewGaugeData("backlog", float64(q.Backlog), "number of bytes currently in queue to be sent.", tags),
		)
	}

	return metrics, nil
}
