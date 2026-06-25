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
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/matcher"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/internal/utils/timeutil"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

const netstatCacheInterval = 30 * time.Second

// holds the raw /proc/net/(netstat|snmp) parse result for one container.
type netstatContainerRaw struct {
	container *pod.Container
	stats     map[string]map[string]string
}

type netstatCollector struct {
	mu      sync.RWMutex
	cache   []netstatContainerRaw
	running atomic.Bool
}

func init() {
	tracing.RegisterEventTracing("netstat", newNetstatCollector)
}

func newNetstatCollector() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &netstatCollector{},
		Interval:    10,
		Flag:        tracing.FlagTracing | tracing.FlagMetric,
	}, nil
}

func (c *netstatCollector) Start(ctx context.Context) error {
	c.running.Store(true)
	defer c.running.Store(false)

	timeutil.RunEvery(ctx, netstatCacheInterval, c.readAndCache)
	return nil
}

func (c *netstatCollector) Update() ([]*metric.Data, error) {
	if !c.running.Load() {
		return nil, nil
	}

	f, err := matcher.NewValueMatcher(cfg.Netstat.Included, cfg.Netstat.Excluded)
	if err != nil {
		return nil, fmt.Errorf("netstat filter: %w", err)
	}

	c.mu.RLock()
	cache := c.cache
	c.mu.RUnlock()

	return buildNetstatMetrics(cache, f), nil
}

func (c *netstatCollector) readAndCache() {
	containers, err := pod.NormalContainers()
	if err != nil {
		log.Warnf("netstat: list containers failed: %v", err)
		return
	}

	// support the host metrics
	if containers == nil {
		containers = make(map[string]*pod.Container)
	}

	// append init namespace into containers
	containers[""] = nil

	var cache []netstatContainerRaw
	for _, container := range containers {
		pid := container.InitPidOrInitnsPid()
		netStats, err := parseNetStat(procfs.Path(strconv.Itoa(pid), "net", "netstat"))
		if err != nil {
			log.Debugf("netstat/snmp metrics for container %v: %v", container, err)
			continue
		}
		snmpStats, err := parseNetStat(procfs.Path(strconv.Itoa(pid), "net", "snmp"))
		if err != nil {
			log.Debugf("netstat/snmp metrics for container %v: %v", container, err)
			continue
		}
		for k, v := range snmpStats {
			netStats[k] = v
		}

		cache = append(cache, netstatContainerRaw{container: container, stats: netStats})
	}

	c.mu.Lock()
	c.cache = cache
	c.mu.Unlock()
}

func buildNetstatMetrics(cache []netstatContainerRaw, f *matcher.ValueMatcher) []*metric.Data {
	var metrics []*metric.Data
	for _, entry := range cache {
		for protocol, protocolStats := range entry.stats {
			for name, value := range protocolStats {
				v, err := strconv.ParseFloat(value, 64)
				if err != nil {
					continue
				}

				key := protocol + "_" + name
				if !f.Match(key) {
					log.Debugf("Ignoring netstat metric %s", key)
					continue
				}

				help := fmt.Sprintf("statistic %s_%s.", protocol, name)
				if entry.container != nil {
					metrics = append(metrics, metric.NewContainerGaugeData(entry.container, key, v, help, nil))
				} else {
					metrics = append(metrics, metric.NewGaugeData(key, v, help, nil))
				}
			}
		}
	}
	return metrics
}

func parseNetStat(fileName string) (map[string]map[string]string, error) {
	file, err := os.Open(fileName)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var (
		stats   = map[string]map[string]string{}
		scanner = bufio.NewScanner(file)
	)

	for scanner.Scan() {
		nameParts := strings.Split(scanner.Text(), " ")
		if len(nameParts) == 0 || nameParts[0] == "" {
			continue
		}

		if !scanner.Scan() {
			break
		}

		valueParts := strings.Split(scanner.Text(), " ")

		// remove trailing ":"
		protocol := nameParts[0][:len(nameParts[0])-1]
		if protocol != "Tcp" && protocol != "TcpExt" {
			continue
		}

		stats[protocol] = map[string]string{}
		if len(nameParts) != len(valueParts) {
			return nil, fmt.Errorf("mismatch: %s:%s", fileName, protocol)
		}

		for i := 1; i < len(nameParts); i++ {
			stats[protocol][nameParts[i]] = valueParts[i]
		}
	}

	return stats, scanner.Err()
}
