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
	"fmt"

	"huatuo-bamai/internal/matcher"
	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"

	promprocfs "github.com/prometheus/procfs"
	"golang.org/x/sys/unix"
)

type mountPointCollector struct{}

func init() {
	tracing.RegisterEventTracing("mountpoint_perm", newMountPoint)
}

func newMountPoint() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &mountPointCollector{},
		Flag:        tracing.FlagMetric,
	}, nil
}

func (c *mountPointCollector) Update() ([]*metric.Data, error) {
	fs, err := procfs.NewDefaultFS()
	if err != nil {
		return nil, err
	}

	mountinfo, err := fs.GetMounts()
	if err != nil {
		return nil, err
	}

	cfg := configSnapshot()
	f, err := matcher.NewValueMatcher(cfg.MountPointStat.MountPointsIncluded, "")
	if err != nil {
		return nil, fmt.Errorf("mount point filter: %w", err)
	}

	metrics := []*metric.Data{}
	for _, v := range mountinfo {
		if !f.Match(v.MountPoint) {
			continue
		}

		var stat unix.Statfs_t
		if err := unix.Statfs(v.MountPoint, &stat); err != nil {
			continue
		}
		metrics = append(metrics, mountPointMetrics(v, &stat)...)
	}
	return metrics, nil
}

func mountPointMetrics(mount *promprocfs.MountInfo, stat *unix.Statfs_t) []*metric.Data {
	capacityLabels := map[string]string{
		"device":     mount.Source,
		"fstype":     mount.FSType,
		"mountpoint": mount.MountPoint,
	}
	readonlyLabels := map[string]string{"mountpoint": mount.MountPoint}
	readonly := 0
	if _, ok := mount.Options["ro"]; ok {
		readonly = 1
	}
	blockSize := float64(stat.Bsize)

	return []*metric.Data{
		metric.NewGaugeData("size_bytes", float64(stat.Blocks)*blockSize,
			"Filesystem size in bytes.", capacityLabels),
		metric.NewGaugeData("free_bytes", float64(stat.Bfree)*blockSize,
			"Filesystem free space in bytes.", capacityLabels),
		metric.NewGaugeData("avail_bytes", float64(stat.Bavail)*blockSize,
			"Filesystem space available to non-root users in bytes.", capacityLabels),
		metric.NewGaugeData("files", float64(stat.Files),
			"Filesystem total file nodes.", capacityLabels),
		metric.NewGaugeData("files_free", float64(stat.Ffree),
			"Filesystem free file nodes.", capacityLabels),
		metric.NewGaugeData("ro", float64(readonly),
			"whether mountpoint is readonly or not", readonlyLabels),
	}
}
