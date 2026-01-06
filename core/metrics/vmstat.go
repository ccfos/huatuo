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
	"os"
	"strconv"
	"strings"

	"huatuo-bamai/internal/conf"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

type vmStatCollector struct{}

func init() {
	tracing.RegisterEventTracing("vmstat", newVMStatCollector)
}

var vmStatMetricDesc = map[string]string{
	"allocstall_normal":     "host direct reclaim count on normal zone",
	"allocstall_movable":    "host direct reclaim count on movable zone",
	"compact_stall":         "memory compaction count",
	"nr_active_anon":        "anonymous pages on active lru",
	"nr_active_file":        "file pages on active lru",
	"nr_boost_pages":        "kswapd boost pages",
	"nr_dirty":              "dirty pages",
	"nr_free_pages":         "free pages in buddy system",
	"nr_inactive_anon":      "anonymous pages on inactive lru",
	"nr_inactive_file":      "file pages on inactive lru",
	"nr_kswapd_boost":       "kswapd boosting count",
	"nr_mlock":              "mlocked pages",
	"nr_shmem":              "shared memory pages",
	"nr_slab_reclaimable":   "reclaimable slab pages",
	"nr_slab_unreclaimable": "unreclaimable slab pages",
	"nr_unevictable":        "unevictable pages",
	"nr_writeback":          "writing-back pages",
	"numa_pages_migrated":   "numa migrated pages",
	"pgdeactivate":          "pages deactivated from active lru to inactive lru",
	"pgrefill":              "pages scanned on active lru",
	"pgscan_direct":         "scanned pages in host direct reclaim",
	"pgscan_kswapd":         "scanned pages in host kswapd reclaim",
	"pgsteal_direct":        "reclaimed pages in host direct reclaim",
	"pgsteal_kswapd":        "reclaimed pages in host kswapd reclaim",
}

func newVMStatCollector() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &vmStatCollector{},
		Flag:        tracing.FlagMetric,
	}, nil
}

func (c *vmStatCollector) Update() ([]*metric.Data, error) {
	filter := newFieldFilter(conf.Get().MetricCollector.Vmstat.Excluded,
		conf.Get().MetricCollector.Vmstat.Included)

	file, err := os.Open("/proc/vmstat")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	var metrics []*metric.Data
	for scanner.Scan() {
		parts := strings.Fields(scanner.Text())
		if filter.ignored(parts[0]) {
			log.Debugf("Ignoring vmstat metric: %s", parts[0])
			continue
		}
		value, err := strconv.ParseFloat(parts[1], 64)
		if err != nil {
			return nil, err
		}
		metrics = append(metrics,
			metric.NewGaugeData(parts[0], value, vmStatMetricDesc[parts[0]], nil))
	}
	return metrics, nil
}
