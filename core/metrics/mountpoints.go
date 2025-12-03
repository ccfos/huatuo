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
	"github.com/prometheus/procfs"

	"huatuo-bamai/internal/conf"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

type mountPointStatCollector struct{}

func init() {
	tracing.RegisterEventTracing("mountpoint_perm", newMountPointStat)
}

func newMountPointStat() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &mountPointStatCollector{},
		Flag:        tracing.FlagMetric,
	}, nil
}

func (c *mountPointStatCollector) Update() ([]*metric.Data, error) {
	mountinfo, err := procfs.GetMounts()
	if err != nil {
		return nil, err
	}

	filter := newFieldFilter("", conf.Get().MetricCollector.MountPointStat.MountPointsIncluded)

	metrics := []*metric.Data{}
	for _, v := range mountinfo {
		if filter.ignored(v.MountPoint) {
			continue
		}

		mountTag := map[string]string{"mountpoint": v.MountPoint}
		ro := 0
		if _, ok := v.Options["ro"]; ok {
			ro = 1
		}

		metrics = append(metrics,
			metric.NewGaugeData("ro", float64(ro), "whether mountpoint is readonly or not", mountTag))
	}
	return metrics, nil
}
