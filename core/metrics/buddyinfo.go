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
	"strconv"

	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

type buddyInfoCollector struct {
	fs procfs.FS
}

func init() {
	tracing.RegisterEventTracing("buddyinfo", newBuddyInfo)
}

func newBuddyInfo() (*tracing.EventTracingAttr, error) {
	fs, err := procfs.NewDefaultFS()
	if err != nil {
		return nil, fmt.Errorf("open procfs: %w", err)
	}

	return &tracing.EventTracingAttr{
		TracingData: &buddyInfoCollector{fs: fs},
		Flag:        tracing.FlagMetric,
	}, nil
}

func (c *buddyInfoCollector) Update() ([]*metric.Data, error) {
	buddyInfo, err := c.fs.BuddyInfo()
	if err != nil {
		return nil, err
	}

	var (
		buddyLabel = make(map[string]string)
		metrics    = []*metric.Data{}
	)

	for _, entry := range buddyInfo {
		for size, value := range entry.Sizes {
			buddyLabel["node"] = entry.Node
			buddyLabel["zone"] = entry.Zone
			buddyLabel["size"] = strconv.Itoa(size)

			metrics = append(metrics,
				metric.NewGaugeData("blocks", value, "buddy info", buddyLabel))
		}
	}

	return metrics, nil
}
