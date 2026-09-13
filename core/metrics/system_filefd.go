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

type fileFDCollector struct{}

func init() {
	tracing.RegisterEventTracing("filefd", newFileFDCollector)
}

func newFileFDCollector() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &fileFDCollector{},
		Flag:        tracing.FlagMetric,
	}, nil
}

func parseFileFD(raw string) (allocated, maximum uint64, err error) {
	fields := strings.Fields(raw)
	if len(fields) != 3 {
		return 0, 0, fmt.Errorf("file-nr has %d fields, want 3", len(fields))
	}
	allocated, err = strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parse allocated file handles: %w", err)
	}
	maximum, err = strconv.ParseUint(fields[2], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parse maximum file handles: %w", err)
	}
	return allocated, maximum, nil
}

func (c *fileFDCollector) Update() ([]*metric.Data, error) {
	raw, err := os.ReadFile(procfs.Path("sys/fs/file-nr"))
	if err != nil {
		return nil, err
	}
	allocated, maximum, err := parseFileFD(string(raw))
	if err != nil {
		return nil, err
	}
	usagePercent := float64(0)
	if maximum > 0 {
		usagePercent = float64(allocated) / float64(maximum) * 100
	}
	return []*metric.Data{
		metric.NewGaugeData("allocated", float64(allocated),
			"Allocated file handles.", nil),
		metric.NewGaugeData("maximum", float64(maximum),
			"Maximum file handles.", nil),
		metric.NewGaugeData("usage_percent", usagePercent,
			"Allocated file handles as a percentage of the maximum.", nil),
	}, nil
}
