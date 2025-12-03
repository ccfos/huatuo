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
//	- netstat_linux.go

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"huatuo-bamai/internal/conf"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pod"
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
	filter := newFieldFilter(conf.Get().MetricCollector.Netstat.Excluded, conf.Get().MetricCollector.Netstat.Included)
	log.Debugf("Updating netstat metrics by filter: %v", filter)

	containers, err := pod.NormalContainers()
	if err != nil {
		return nil, err
	}

	// support the empty container
	if containers == nil {
		containers = make(map[string]*pod.Container)
	}
	// append host into containers
	containers[""] = nil

	var metrics []*metric.Data
	for _, container := range containers {
		m, err := c.getStatMetrics(container, filter)
		if err != nil {
			return nil, fmt.Errorf("couldn't get netstat metrics for container %v: %w", container, err)
		}
		metrics = append(metrics, m...)
	}

	log.Debugf("Updated netstat metrics by filter %v: %v", filter, metrics)
	return metrics, nil
}

func (c *netstatCollector) getStatMetrics(container *pod.Container, filter *fieldFilter) ([]*metric.Data, error) {
	pid := 1 // host
	if container != nil {
		pid = container.InitPid
	}

	pidProc := filepath.Join("/proc", strconv.Itoa(pid))
	netStats, err := c.procNetstats(filepath.Join(pidProc, "net/netstat"))
	if err != nil {
		return nil, fmt.Errorf("couldn't get netstats for %v: %w", container, err)
	}
	snmpStats, err := c.procNetstats(filepath.Join(pidProc, "net/snmp"))
	if err != nil {
		return nil, fmt.Errorf("couldn't get SNMP stats for %v: %w", container, err)
	}

	// Merge the results of snmpStats into netStats (collisions are possible, but
	// we know that the keys are always unique for the given use case).
	for k, v := range snmpStats {
		netStats[k] = v
	}

	var metrics []*metric.Data
	for protocol, protocolStats := range netStats {
		for name, value := range protocolStats {
			key := protocol + "_" + name
			v, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid value %s in netstats for %v: %w", value, container, err)
			}

			if filter.ignored(key) {
				log.Debugf("Ignoring netstat metric %s", key)
				continue
			}

			if container != nil {
				metrics = append(metrics,
					metric.NewContainerGaugeData(container, key, v, fmt.Sprintf("Statistic %s.", protocol+name), nil))
			} else {
				metrics = append(metrics,
					metric.NewGaugeData(key, v, fmt.Sprintf("Statistic %s.", protocol+name), nil))
			}
		}
	}

	return metrics, nil
}

func (c *netstatCollector) procNetstats(fileName string) (map[string]map[string]string, error) {
	file, err := os.Open(fileName)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var (
		netStats = map[string]map[string]string{}
		scanner  = bufio.NewScanner(file)
	)

	for scanner.Scan() {
		nameParts := strings.Split(scanner.Text(), " ")
		scanner.Scan()
		valueParts := strings.Split(scanner.Text(), " ")
		// Remove trailing :.
		protocol := nameParts[0][:len(nameParts[0])-1]

		// protocol: only for Tcp/TcpExt
		if protocol != "Tcp" && protocol != "TcpExt" {
			continue
		}

		netStats[protocol] = map[string]string{}
		if len(nameParts) != len(valueParts) {
			return nil, fmt.Errorf("mismatch field count mismatch in %s: %s",
				fileName, protocol)
		}
		for i := 1; i < len(nameParts); i++ {
			netStats[protocol][nameParts[i]] = valueParts[i]
		}
	}

	return netStats, scanner.Err()
}
