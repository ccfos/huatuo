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
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"huatuo-bamai/internal/pod"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

var arpCachePath = "/proc/net/stat/arp_cache"

type arpCollector struct {
	metric []*metric.Data
}

func init() {
	tracing.RegisterEventTracing("arp", newArp)
}

func newArp() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &arpCollector{
			metric: []*metric.Data{
				metric.NewGaugeData("entries", 0, "host init namespace", nil),
				metric.NewGaugeData("total", 0, "arp_cache entries", nil),
			},
		},
		Flag: tracing.FlagMetric,
	}, nil
}

// NetStat contains statistics for all the counters from one file.
// should be exported for /proc/net/stat/ndisc_cache
type NetStat struct {
	Stats    map[string]uint64
	Filename string
}

func parseNetstatCache(filePath string) (NetStat, error) {
	netStat := NetStat{
		Stats: make(map[string]uint64),
	}

	file, err := os.Open(filePath)
	if err != nil {
		return netStat, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Scan()

	// First string is always a header for stats
	var headers []string
	headers = append(headers, strings.Fields(scanner.Text())...)

	// Fast path ...
	scanner.Scan()
	for num, counter := range strings.Fields(scanner.Text()) {
		value, err := strconv.ParseUint(counter, 16, 64)
		if err != nil {
			return NetStat{}, err
		}
		netStat.Stats[headers[num]] = value
	}

	return netStat, nil
}

func (c *arpCollector) updateHostArp() []*metric.Data {
	count, err := fileLineCounter("/proc/1/net/arp")
	if err != nil {
		return nil
	}

	stat, err := parseNetstatCache(arpCachePath)
	if err != nil {
		return nil
	}

	c.metric[0].Value = float64(count - 1)
	c.metric[1].Value = float64(stat.Stats["entries"])

	return c.metric
}

func (c *arpCollector) Update() ([]*metric.Data, error) {
	data := []*metric.Data{}

	containers, err := pod.NormalContainers()
	if err != nil {
		return nil, fmt.Errorf("GetNormalContainers: %w", err)
	}

	for _, container := range containers {
		count, err := fileLineCounter(fmt.Sprintf("/proc/%d/net/arp", container.InitPid))
		if err != nil {
			return nil, err
		}

		data = append(data, metric.NewContainerGaugeData(container, "entries", float64(count-1), "arp for container and host", nil))
	}

	return append(data, c.updateHostArp()...), nil
}
