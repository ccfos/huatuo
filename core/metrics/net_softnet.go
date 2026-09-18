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

// ref: https://github.com/prometheus/node_exporter/blob/master/collector/softnet_linux.go
// ref: https://github.com/netdata/netdata/blob/master/src/collectors/proc.plugin/proc_softnet.c
//
// /proc/net/softnet_stat, produced by net/core/net-procfs.c
// (softnet_seq_show) since Linux 2.6.23: one hexadecimal row per CPU with
// the columns processed, dropped, time_squeeze, unused (cpu collision),
// received_rps (0 without CONFIG_RPS), flow_limit_count (0 without
// CONFIG_NET_FLOW_LIMIT), then backlog/dropped/time_squeeze duplicates.

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

type netSoftnet struct{}

func init() {
	tracing.RegisterEventTracing("softnet", newNetSoftnet)
}

func newNetSoftnet() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &netSoftnet{},
		Flag:        tracing.FlagMetric,
	}, nil
}

type softnetCPUStat struct {
	index        int
	processed    uint64
	dropped      uint64
	timeSqueezed uint64
	receivedRps  uint64
}

func parseSoftnetStat(reader io.Reader) ([]softnetCPUStat, error) {
	var (
		stats   []softnetCPUStat
		scanner = bufio.NewScanner(reader)
	)

	for scanner.Scan() {
		columns := strings.Fields(scanner.Text())
		if len(columns) < 9 {
			return nil, fmt.Errorf("softnet_stat: expected at least 9 hex columns, got %d", len(columns))
		}

		stat := softnetCPUStat{index: len(stats)}

		var err error
		if stat.processed, err = strconv.ParseUint(columns[0], 16, 64); err != nil {
			return nil, fmt.Errorf("softnet_stat: parse processed: %w", err)
		}
		if stat.dropped, err = strconv.ParseUint(columns[1], 16, 64); err != nil {
			return nil, fmt.Errorf("softnet_stat: parse dropped: %w", err)
		}
		if stat.timeSqueezed, err = strconv.ParseUint(columns[2], 16, 64); err != nil {
			return nil, fmt.Errorf("softnet_stat: parse time_squeeze: %w", err)
		}
		if stat.receivedRps, err = strconv.ParseUint(columns[4], 16, 64); err != nil {
			return nil, fmt.Errorf("softnet_stat: parse received_rps: %w", err)
		}

		stats = append(stats, stat)
	}

	return stats, scanner.Err()
}

func (c *netSoftnet) Update() ([]*metric.Data, error) {
	file, err := os.Open(procfs.Path("net", "softnet_stat"))
	if err != nil {
		return nil, err
	}
	defer file.Close()

	stats, err := parseSoftnetStat(file)
	if err != nil {
		return nil, err
	}

	metrics := make([]*metric.Data, 0, len(stats)*4)
	for _, stat := range stats {
		labels := map[string]string{"cpu": strconv.Itoa(stat.index)}
		metrics = append(metrics,
			metric.NewCounterData("processed_total", float64(stat.processed),
				"Total number of network packets processed by this CPU.", labels),
			metric.NewCounterData("dropped_total", float64(stat.dropped),
				"Total number of network packets dropped because this CPU's backlog queue was full.", labels),
			metric.NewCounterData("time_squeezed_total", float64(stat.timeSqueezed),
				"Total number of times net_rx processing on this CPU ran out of its softirq budget.", labels),
			metric.NewCounterData("received_rps_total", float64(stat.receivedRps),
				"Total number of times this CPU was woken by an IPI to process received packets (RPS).", labels),
		)
	}

	return metrics, nil
}
