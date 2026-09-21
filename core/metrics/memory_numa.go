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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ccfos/huatuo/internal/procfs/sysfs"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/internal/utils/parseutil"
	"github.com/ccfos/huatuo/pkg/metric"
)

type memoryNUMACollector struct{}

var numaCounters = [...]struct{ field, help string }{
	{"numa_hit", "Pages allocated on this node when it was the preferred node."},
	{"numa_miss", "Pages allocated on this node when another node was preferred."},
	{"numa_foreign", "Pages allocated on another node when this node was preferred."},
	{"interleave_hit", "Pages allocated on this node according to the interleave policy."},
	{"local_node", "Pages allocated on this node by a CPU on the same node."},
	{"other_node", "Pages allocated on this node by a CPU on another node."},
}

func init() {
	tracing.RegisterEventTracing("memory_numa", newMemoryNUMA)
}

func newMemoryNUMA() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{TracingData: &memoryNUMACollector{}, Flag: tracing.FlagMetric}, nil
}

func (*memoryNUMACollector) Update() ([]*metric.Data, error) {
	// Rediscover nodes each scrape because memory nodes can be hot-added or removed.
	paths, err := filepath.Glob(sysfs.Path("devices/system/node/node[0-9]*/numastat"))
	if err != nil {
		return nil, err
	}
	metrics := make([]*metric.Data, 0, len(paths)*len(numaCounters))
	var collectErr error
	for _, path := range paths {
		values, err := parseutil.RawKV(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			collectErr = errors.Join(collectErr, fmt.Errorf("read NUMA statistics %s: %w", path, err))
			continue
		}
		node := strings.TrimPrefix(filepath.Base(filepath.Dir(path)), "node")
		labels := map[string]string{"node": node}
		for _, counter := range numaCounters {
			if value, exists := values[counter.field]; exists {
				metrics = append(metrics, metric.NewCounterData(counter.field+"_pages_total", float64(value), counter.help, labels))
			}
		}
	}
	if len(metrics) == 0 && collectErr == nil {
		return nil, metric.ErrNoData
	}
	return metrics, collectErr
}
