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
	"strconv"

	"github.com/ccfos/huatuo/internal/matcher"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/metric"
	"github.com/ccfos/huatuo/pkg/types"
)

type netdevIPv6Collector struct{}

func init() {
	tracing.RegisterEventTracing("netdev_ipv6", newNetdevIPv6Collector)
}

func newNetdevIPv6Collector() (*tracing.EventTracingAttr, error) {
	if err := procfs.RequireFile("1", "net", "dev_snmp6"); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, types.ErrNotSupported
		}
		return nil, fmt.Errorf("check IPv6 interface statistics: %w", err)
	}
	return &tracing.EventTracingAttr{TracingData: &netdevIPv6Collector{}, Flag: tracing.FlagMetric}, nil
}

func (c *netdevIPv6Collector) Update() ([]*metric.Data, error) {
	containers, err := pod.NormalContainers()
	if err != nil {
		return nil, err
	}
	if containers == nil {
		containers = make(map[string]*pod.Container)
	}
	containers[""] = nil
	cfg := configSnapshot()
	filter, err := matcher.NewValueMatcher(cfg.NetdevStats.DeviceIncluded, cfg.NetdevStats.DeviceExcluded)
	if err != nil {
		return nil, fmt.Errorf("IPv6 interface filter: %w", err)
	}
	var metrics []*metric.Data
	var errs []error
	for _, container := range containers {
		data, err := c.namespaceStats(container, filter)
		metrics = append(metrics, data...)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return metrics, errors.Join(errs...)
}

func (c *netdevIPv6Collector) namespaceStats(container *pod.Container, filter *matcher.ValueMatcher) ([]*metric.Data, error) {
	pid := container.InitPidOrInitnsPid()
	procFS, err := procfs.NewFS(procfs.Path(strconv.Itoa(pid)))
	if err != nil {
		return nil, fmt.Errorf("open IPv6 statistics for pid %d: %w", pid, err)
	}
	stats, readErr := procFS.NetDevSNMP6()
	if errors.Is(readErr, fs.ErrNotExist) && len(stats) == 0 {
		return nil, nil
	}
	var metrics []*metric.Data
	for device, counters := range stats {
		if !filter.Match(device) {
			continue
		}
		labels := map[string]string{"device": device}
		for name, value := range counters {
			if name == "ifIndex" {
				continue
			}
			help := fmt.Sprintf("IPv6 interface statistic %s.", name)
			if container == nil {
				metrics = append(metrics, metric.NewCounterData(name+"_total", float64(value), help, labels))
			} else {
				metrics = append(metrics, metric.NewContainerCounterData(container, name+"_total", float64(value), help, labels))
			}
		}
	}
	if readErr != nil {
		return metrics, fmt.Errorf("read IPv6 statistics for pid %d: %w", pid, readErr)
	}
	return metrics, nil
}
