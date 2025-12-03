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
	"huatuo-bamai/internal/conf"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"

	"github.com/ema/qdisc"
)

type qdiscStats struct {
	ifaceName  string
	kind       string
	bytes      uint64
	packets    uint32
	drops      uint32
	requeues   uint32
	overlimits uint32
	qlen       uint32
	backlog    uint32
}

const tcHMajMask = 0xFFFF0000

type qdiscCollector struct{}

func init() {
	tracing.RegisterEventTracing("netdev_qdisc", newQdiscCollector)
}

func newQdiscCollector() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &qdiscCollector{},
		Flag:        tracing.FlagMetric,
	}, nil
}

// sum of same level(parent major) for a device, example:
// <device0> (1+2, 3)
// 1: qidsc <kind> handle0 parent0
// 2: qidsc <kind> handle1 parent0
// 3: qidsc <kind> handle2 parent1
//
// <device1> (1, 2+3)
// 1: qidsc <kind> handle0 parent0
// 2: qidsc <kind> handle1 parent1
// 3: qidsc <kind> handle2 parent1
func (c *qdiscCollector) Update() ([]*metric.Data, error) {
	filter := newFieldFilter(conf.Get().MetricCollector.Qdisc.DeviceExcluded,
		conf.Get().MetricCollector.Qdisc.DeviceIncluded)

	allQdisc, err := qdisc.Get()
	if err != nil {
		return nil, err
	}

	allQdiscMap := make(map[string]map[uint32]*qdiscStats)
	for _, q := range allQdisc {
		if filter.ignored(q.IfaceName) || q.Kind == "noqueue" {
			continue
		}

		parentMaj := (q.Parent & tcHMajMask) >> 16
		if _, ok := allQdiscMap[q.IfaceName]; !ok {
			allQdiscMap[q.IfaceName] = make(map[uint32]*qdiscStats)
		}
		netQdisc, ok := allQdiscMap[q.IfaceName][parentMaj]
		if !ok {
			allQdiscMap[q.IfaceName][parentMaj] = &qdiscStats{
				ifaceName:  q.IfaceName,
				kind:       q.Kind,
				bytes:      q.Bytes,
				packets:    q.Packets,
				drops:      q.Drops,
				requeues:   q.Requeues,
				overlimits: q.Overlimits,
				qlen:       q.Qlen,
				backlog:    q.Backlog,
			}
		} else {
			netQdisc.bytes += q.Bytes
			netQdisc.packets += q.Packets
			netQdisc.drops += q.Drops
			netQdisc.requeues += q.Requeues
			netQdisc.overlimits += q.Overlimits
			netQdisc.qlen += q.Qlen
			netQdisc.backlog += q.Backlog
		}
	}

	var metrics []*metric.Data
	for _, netdevQdisc := range allQdiscMap {
		for _, oneQdisc := range netdevQdisc {
			tags := map[string]string{"device": oneQdisc.ifaceName, "kind": oneQdisc.kind}
			metrics = append(metrics,
				metric.NewCounterData("bytes_total", float64(oneQdisc.bytes),
					"Number of bytes sent.", tags),
				metric.NewCounterData("packets_total", float64(oneQdisc.packets),
					"Number of packets sent.", tags),
				metric.NewCounterData("drops_total", float64(oneQdisc.drops),
					"Number of packet drops.", tags),
				metric.NewCounterData("requeues_total", float64(oneQdisc.requeues),
					"Number of packets dequeued, not transmitted, and requeued.", tags),
				metric.NewCounterData("overlimits_total", float64(oneQdisc.overlimits),
					"Number of packet overlimits.", tags),
				metric.NewGaugeData("current_queue_length", float64(oneQdisc.qlen),
					"Number of packets currently in queue to be sent.", tags),
				metric.NewGaugeData("backlog", float64(oneQdisc.backlog),
					"Number of bytes currently in queue to be sent.", tags),
			)
		}
	}

	return metrics, nil
}
