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
//	- netstat_linux.go

import (
	"fmt"
	"strconv"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/matcher"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

type netstatCollector struct{}

func init() {
	tracing.RegisterEventTracing("netstat", newNetstatCollector)
}

func newNetstatCollector() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &netstatCollector{},
		Flag:        tracing.FlagMetric,
	}, nil
}

func (c *netstatCollector) Update() ([]*metric.Data, error) {
	containers, err := pod.NormalContainers()
	if err != nil {
		return nil, err
	}

	// support the host metrics
	if containers == nil {
		containers = make(map[string]*pod.Container)
	}

	// append init namespace into containers
	containers[""] = nil

	cfg := configSnapshot()
	f, err := matcher.NewValueMatcher(cfg.Netstat.Included, cfg.Netstat.Excluded)
	if err != nil {
		return nil, fmt.Errorf("netstat filter: %w", err)
	}

	var metrics []*metric.Data
	for _, container := range containers {
		m, err := buildNetAndSnmpStat(container, f)
		if err != nil {
			log.Errorf("netstat/snmp metrics for container %v: %v", container, err)
			continue
		}
		metrics = append(metrics, m...)
	}
	log.Debugf("Updated netstat metrics by filter %v: %v", f, metrics)
	return metrics, nil
}

func buildNetAndSnmpStat(container *pod.Container, f *matcher.ValueMatcher) ([]*metric.Data, error) {
	pid := container.InitPidOrInitnsPid()

	netStats, err := parseNetStat(procfs.Path(strconv.Itoa(pid), "net", "netstat"))
	if err != nil {
		return nil, err
	}

	snmpStats, err := parseNetStat(procfs.Path(strconv.Itoa(pid), "net", "snmp"))
	if err != nil {
		return nil, err
	}

	// Merge the results of snmpStats into netStats (collisions are possible, but
	// we know that the keys are always unique for the given use case).
	for k, v := range snmpStats {
		netStats[k] = v
	}

	var metrics []*metric.Data
	for protocol, protocolStats := range netStats {
		for name, value := range protocolStats {
			v, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return nil, err
			}

			key := protocol + "_" + name
			if !f.Match(key) {
				log.Debugf("Ignoring netstat metric %s", key)
				continue
			}

			if container != nil {
				metrics = append(metrics,
					metric.NewContainerGaugeData(container, key, v, fmt.Sprintf("statistic %s.", protocol+name), nil))
			} else {
				metrics = append(metrics,
					metric.NewGaugeData(key, v, fmt.Sprintf("statistic %s.", protocol+name), nil))
			}
		}
	}

	return metrics, nil
}
