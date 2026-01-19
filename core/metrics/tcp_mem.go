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
	"fmt"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

const (
	skMemQuantum = 4096
)

type tcpMemCollector struct {
	tcpMemMetric []*metric.Data
}

func init() {
	tracing.RegisterEventTracing("tcp_mem", newTCPMemCollector)
}

func newTCPMemCollector() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &tcpMemCollector{
			tcpMemMetric: []*metric.Data{
				metric.NewGaugeData("usage_pages", 0, "tcp mem usage(pages)", nil),
				metric.NewGaugeData("usage_bytes", 0, "tcp mem usage(bytes)", nil),
				metric.NewGaugeData("limit_pages", 0, "tcp mem limit(pages)", nil),
				metric.NewGaugeData("usage_percent", 0, "tcp mem usage percent", nil),
			},
		},
		Flag: tracing.FlagMetric,
	}, nil
}

func (c *tcpMemCollector) getTCPMem() (tcpMem, tcpMemBytes, tcpMemLimit float64, err error) {
	fs, err := procfs.NewDefaultFS()
	if err != nil {
		log.Infof("failed to open sysfs: %v", err)
		return -1, -1, -1, err
	}

	values, err := fs.SysctlInts("net.ipv4.tcp_mem")
	if err != nil {
		log.Infof("error obtaining sysctl info: %v", err)
		return -1, -1, -1, err
	}

	tcpMemLimit = float64(values[2])

	stat4, err := fs.NetSockstat()
	if err != nil {
		log.Infof("failed to get NetSockstat: %v", err)
		return -1, -1, -1, err
	}

	for _, p := range stat4.Protocols {
		if p.Protocol != "TCP" {
			continue
		}

		if p.Mem == nil {
			return -1, -1, -1, fmt.Errorf("failed to read tcpmem usage")
		}

		tcpMem = float64(*p.Mem)
		tcpMemBytes = float64(*p.Mem * skMemQuantum)
	}

	return tcpMem, tcpMemBytes, tcpMemLimit, nil
}

func (c *tcpMemCollector) Update() ([]*metric.Data, error) {
	tcpMem, tcpMemBytes, tcpMemLimit, err := c.getTCPMem()
	if err != nil {
		return nil, err
	}

	c.tcpMemMetric[0].Value = tcpMem
	c.tcpMemMetric[1].Value = tcpMemBytes
	c.tcpMemMetric[2].Value = tcpMemLimit
	c.tcpMemMetric[3].Value = tcpMem / tcpMemLimit

	return c.tcpMemMetric, nil
}
