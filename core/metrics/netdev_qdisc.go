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
	deviceMatcher *matcher.ValueMatcher
	// Keeping rtnetlink I/O replaceable makes Update independent of host qdisc state in tests.
	readStats func() ([]qdisc.Stats, error)
}

const metricsPerQdisc = 7

func init() {
	tracing.RegisterEventTracing("netdev_qdisc", newQdiscCollector)
}

func newQdiscCollector() (*tracing.EventTracingAttr, error) {
	cfg := configSnapshot()
	deviceMatcher, err := matcher.NewValueMatcher(
		cfg.Qdisc.DeviceIncluded,
		cfg.Qdisc.DeviceExcluded,
	)
	if err != nil {
		return nil, fmt.Errorf("qdisc device filter: %w", err)
	}

	return &tracing.EventTracingAttr{
		TracingData: &qdiscCollector{
			deviceMatcher: deviceMatcher,
			readStats:     qdisc.Read,
		},
		Flag: tracing.FlagMetric,
	}, nil
}

func (c *qdiscCollector) Update() ([]*metric.Data, error) {
	stats, err := c.readStats()
	if err != nil {
		return nil, fmt.Errorf("read qdisc statistics: %w", err)
	}

	metrics := make([]*metric.Data, 0, len(stats)*metricsPerQdisc)
	labels := map[string]string{"device": "", "kind": ""}
	for i := range stats {
		stat := &stats[i]
		if !c.deviceMatcher.Match(stat.Netdev) || stat.Kind == "noqueue" || !stat.IsRoot() {
			continue
		}

		labels["device"] = stat.Netdev
		labels["kind"] = stat.Kind
		// Metric constructors copy labels, so one map serves the entire scrape.
		metrics = appendQdiscMetrics(metrics, stat, labels)
	}

	return metrics, nil
}

func appendQdiscMetrics(
	metrics []*metric.Data,
	stat *qdisc.Stats,
	labels map[string]string,
) []*metric.Data {
	return append(
		metrics,
		metric.NewCounterData(
			"bytes_total",
			float64(stat.Bytes),
			"number of bytes sent.",
			labels,
		),
		metric.NewCounterData(
			"packets_total",
			float64(stat.Packets),
			"number of packets sent.",
			labels,
		),
		metric.NewCounterData(
			"drops_total",
			float64(stat.Drops),
			"number of packet drops.",
			labels,
		),
		metric.NewCounterData(
			"requeues_total",
			float64(stat.Requeues),
			"number of packets dequeued, not transmitted, and requeued.",
			labels,
		),
		metric.NewCounterData(
			"overlimits_total",
			float64(stat.Overlimits),
			"number of packet overlimits.",
			labels,
		),
		metric.NewGaugeData(
			"current_queue_length",
			float64(stat.QueueLength),
			"number of packets currently in queue to be sent.",
			labels,
		),
		metric.NewGaugeData(
			"backlog",
			float64(stat.BacklogBytes),
			"number of bytes currently in queue to be sent.",
			labels,
		),
	)
}
