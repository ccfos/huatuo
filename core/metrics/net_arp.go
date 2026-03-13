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
	"strconv"

	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

type arpCollector struct{}

func init() {
	tracing.RegisterEventTracing("arp", newArp)
}

func newArp() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &arpCollector{},
		Flag:        tracing.FlagMetric,
	}, nil
}

func nodeArpCacheEntries() ([]*metric.Data, error) {
	count, err := CountLines(procfs.Path("1/net/arp"))
	if err != nil {
		return nil, err
	}

	cache, err := procfs.NetArpCache()
	if err != nil {
		return nil, err
	}

	return []*metric.Data{
		metric.NewGaugeData("entries", float64(count-1), "host init namespace", nil),
		metric.NewGaugeData("total", float64(cache.Stats["entries"]), "all entries in arp_cache for containers and host netns", nil),
	}, nil
}

func (c *arpCollector) Update() ([]*metric.Data, error) {
	var data []*metric.Data

	containers, err := pod.NormalContainers()
	if err != nil {
		return nil, err
	}

	for _, container := range containers {
		count, err := CountLines(procfs.Path(strconv.Itoa(container.InitPid), "net/arp"))
		if err != nil {
			// return data collected
			return data, err
		}

		data = append(data, metric.NewContainerGaugeData(container, "entries", float64(count-1), "arp entries in container netns", nil))
	}

	entries, err := nodeArpCacheEntries()
	if err != nil {
		return data, err
	}

	return append(data, entries...), nil
}
