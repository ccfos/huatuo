// Copyright 2025, 2026 The HuaTuo Authors
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

	"github.com/ccfos/huatuo/internal/matcher"

	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/internal/utils/parseutil"
	"github.com/ccfos/huatuo/pkg/metric"
)

type memEventsCollector struct{}

func init() {
	tracing.RegisterEventTracing("memory_events", newMemEvents)
}

func newMemEvents() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &memEventsCollector{}, Flag: tracing.FlagMetric,
	}, nil
}

func (c *memEventsCollector) Update() ([]*metric.Data, error) {
	cfg := configSnapshot()
	f, err := matcher.NewValueMatcher(cfg.MemoryEvents.Included, cfg.MemoryEvents.Excluded)
	if err != nil {
		return nil, fmt.Errorf("memory events filter: %w", err)
	}

	workloads, err := pod.Pods()
	if err != nil {
		return nil, fmt.Errorf("get workloads: %w", err)
	}

	metrics := []*metric.Data{}
	for _, workload := range workloads {
		if !isMemoryWorkload(workload) {
			continue
		}
		directory, err := workload.Cgroups.Directory("memory")
		if err != nil {
			continue
		}
		raw, err := parseutil.RawKV(filepath.Join(directory, "memory.events"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}

		for key, value := range raw {
			if !f.Match(key) {
				continue
			}

			metrics = append(metrics,
				metric.NewPodGaugeData(workload, key, float64(value), fmt.Sprintf("memory events %s", key), nil))
		}
	}

	return metrics, nil
}
