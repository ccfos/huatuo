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

	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

type tcpMemory struct{}

func init() {
	tracing.RegisterEventTracing("tcp_memory", newTcpMemory)
}

func newTcpMemory() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &tcpMemory{},
		Flag:        tracing.FlagMetric,
	}, nil
}

type tcpMemoryStat struct {
	memoryPages float64
	memoryBytes float64
	memoryLimit float64
}

func parseTcpMemory() (*tcpMemoryStat, error) {
	fs, err := procfs.NewDefaultFS()
	if err != nil {
		return nil, err
	}

	values, err := fs.SysctlInts("net.ipv4.tcp_mem")
	if err != nil {
		return nil, err
	}

	stat4, err := fs.NetSockstat()
	if err != nil {
		return nil, err
	}

	for _, p := range stat4.Protocols {
		if p.Protocol != "TCP" {
			continue
		}

		if p.Mem != nil {
			return &tcpMemoryStat{
				memoryPages: float64(*p.Mem),
				memoryBytes: float64(*p.Mem * 4096),
				memoryLimit: float64(values[2]), // tcpMemLimit
			}, nil
		}

		break
	}

	return nil, fmt.Errorf("not found")
}

func (c *tcpMemory) Update() ([]*metric.Data, error) {
	stats, err := parseTcpMemory()
	if err != nil {
		return nil, err
	}

	return []*metric.Data{
		metric.NewGaugeData("usage_pages", stats.memoryPages, "tcp memory pages usage", nil),
		metric.NewGaugeData("usage_bytes", stats.memoryBytes, "tcp memory bytes usage", nil),
		metric.NewGaugeData("limit_pages", stats.memoryLimit, "tcp memory pages limit", nil),
		metric.NewGaugeData("usage_percent", stats.memoryPages/stats.memoryLimit, "tcp memory usage percent", nil),
	}, nil
}
