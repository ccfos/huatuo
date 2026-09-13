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
	"fmt"
	"os"
	"strconv"
	"strings"

	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

type uptimeCollector struct{}

func init() {
	tracing.RegisterEventTracing("uptime", newUptimeCollector)
}

func newUptimeCollector() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &uptimeCollector{},
		Flag:        tracing.FlagMetric,
	}, nil
}

func parseUptime(raw string) (float64, error) {
	fields := strings.Fields(raw)
	if len(fields) != 2 {
		return 0, fmt.Errorf("uptime has %d fields, want 2", len(fields))
	}
	uptime, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, fmt.Errorf("parse uptime: %w", err)
	}
	if _, err := strconv.ParseFloat(fields[1], 64); err != nil {
		return 0, fmt.Errorf("parse idle time: %w", err)
	}
	if uptime < 0 {
		return 0, fmt.Errorf("uptime must not be negative")
	}
	return uptime, nil
}

func (c *uptimeCollector) Update() ([]*metric.Data, error) {
	raw, err := os.ReadFile(procfs.Path("uptime"))
	if err != nil {
		return nil, err
	}
	uptime, err := parseUptime(string(raw))
	if err != nil {
		return nil, err
	}
	return []*metric.Data{
		metric.NewGaugeData("seconds", uptime,
			"Seconds since the system booted.", nil),
	}, nil
}
