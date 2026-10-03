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

// ref: https://github.com/prometheus/node_exporter/blob/master/collector/conntrack_linux.go
// ref: https://github.com/flashcatcloud/categraf/blob/main/inputs/conntrack/conntrack.go

import (
	"errors"
	"fmt"
	"io/fs"

	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
	"huatuo-bamai/pkg/types"
)

type netConntrack struct{}

func init() {
	tracing.RegisterEventTracing("conntrack", newNetConntrack)
}

func newNetConntrack() (*tracing.EventTracingAttr, error) {
	if err := procfs.RequireFile("sys/net/netfilter/nf_conntrack_count"); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, types.ErrNotSupported
		}

		return nil, fmt.Errorf("check conntrack statistics support: %w", err)
	}

	return &tracing.EventTracingAttr{
		TracingData: &netConntrack{},
		Flag:        tracing.FlagMetric,
	}, nil
}

type conntrackStat struct {
	entries float64
	limit   float64
}

func parseConntrack() (*conntrackStat, error) {
	fs, err := procfs.NewDefaultFS()
	if err != nil {
		return nil, err
	}

	count, err := fs.SysctlInts("net.netfilter.nf_conntrack_count")
	if err != nil {
		return nil, fmt.Errorf("read conntrack count: %w", err)
	}

	limit, err := fs.SysctlInts("net.netfilter.nf_conntrack_max")
	if err != nil {
		return nil, fmt.Errorf("read conntrack limit: %w", err)
	}

	if len(count) != 1 || len(limit) != 1 {
		return nil, fmt.Errorf("unexpected conntrack sysctl format: count=%v limit=%v", count, limit)
	}

	return &conntrackStat{
		entries: float64(count[0]),
		limit:   float64(limit[0]),
	}, nil
}

func (c *netConntrack) Update() ([]*metric.Data, error) {
	stats, err := parseConntrack()
	if err != nil {
		return nil, err
	}

	// Sibling *_percent metrics (cpu_stat wait_sum_percent, disk_io
	// disk_iowait_percent) report 0-100, so alert thresholds such as >90
	// fire as expected.
	usagePercent := float64(0)
	if stats.limit > 0 {
		usagePercent = stats.entries / stats.limit * 100
	}

	return []*metric.Data{
		metric.NewGaugeData("entries", stats.entries, "currently tracked connections in the kernel conntrack table", nil),
		metric.NewGaugeData("entries_limit", stats.limit, "conntrack table capacity", nil),
		metric.NewGaugeData("usage_percent", usagePercent, "conntrack table usage as a percentage of its limit (0-100)", nil),
	}, nil
}
