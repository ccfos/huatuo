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

// ref: https://github.com/prometheus/node_exporter/blob/master/collector/netstat_linux.go
// ref: https://github.com/influxdata/telegraf/blob/master/plugins/inputs/nstat/nstat.go

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

type netUdp struct{}

func init() {
	tracing.RegisterEventTracing("udp", newNetUdp)
}

func newNetUdp() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &netUdp{},
		Flag:        tracing.FlagMetric,
	}, nil
}

// udpFields maps the /proc/net/snmp Udp section fields to exported metrics.
// Fields absent on the running kernel are skipped at export time.
var udpFields = []struct {
	key  string
	name string
	help string
}{
	{
		"InDatagrams", "datagrams_received_total",
		"Total number of UDP datagrams delivered to userspace.",
	},
	{
		"OutDatagrams", "datagrams_sent_total",
		"Total number of UDP datagrams sent.",
	},
	{
		"InErrors", "in_errors_total",
		"Total number of UDP datagrams dropped for reasons other than a full receive buffer, e.g. invalid checksum.",
	},
	{
		"NoPorts", "no_ports_total",
		"Total number of UDP datagrams received with no application listening on the destination port.",
	},
	{
		"RcvbufErrors", "rcvbuf_errors_total",
		"Total number of UDP datagrams dropped because the receiving socket buffer was full.",
	},
	{
		"SndbufErrors", "sndbuf_errors_total",
		"Total number of UDP datagrams dropped because the sending socket buffer was full.",
	},
	{
		"InCsumErrors", "in_csum_errors_total",
		"Total number of UDP datagrams dropped because of an invalid checksum.",
	},
}

// parseUdpSnmp parses the "Udp:" header/value line pair from
// /proc/net/snmp. Other protocol sections are skipped without consuming
// their value lines, so a truncated or empty section cannot desync the
// parser.
func parseUdpSnmp(fileName string) (map[string]uint64, error) {
	file, err := os.Open(fileName)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var (
		stats   = map[string]uint64{}
		scanner = bufio.NewScanner(file)
	)

	for scanner.Scan() {
		nameParts := strings.Fields(scanner.Text())
		if len(nameParts) < 2 || nameParts[0] != "Udp:" {
			continue
		}

		if !scanner.Scan() {
			break
		}

		valueParts := strings.Fields(scanner.Text())
		if len(valueParts) < 2 || valueParts[0] != "Udp:" {
			return nil, fmt.Errorf("udp: malformed value line in %s", fileName)
		}

		if len(nameParts) != len(valueParts) {
			return nil, fmt.Errorf("udp: field mismatch in %s", fileName)
		}

		for i := 1; i < len(nameParts); i++ {
			value, err := strconv.ParseUint(valueParts[i], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("udp: parse %s in %s: %w", nameParts[i], fileName, err)
			}

			stats[nameParts[i]] = value
		}
	}

	return stats, scanner.Err()
}

func (c *netUdp) Update() ([]*metric.Data, error) {
	stats, err := parseUdpSnmp(procfs.Path("net", "snmp"))
	if err != nil {
		return nil, err
	}

	metrics := make([]*metric.Data, 0, len(udpFields))
	for _, field := range udpFields {
		value, ok := stats[field.key]
		if !ok {
			continue
		}

		metrics = append(metrics, metric.NewCounterData(field.name, float64(value), field.help, nil))
	}

	if len(metrics) == 0 {
		return nil, metric.ErrNoData
	}

	return metrics, nil
}
