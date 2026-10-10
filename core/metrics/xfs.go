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
	"io/fs"

	"github.com/ccfos/huatuo/internal/procfs"
	xfsproc "github.com/ccfos/huatuo/internal/procfs/xfs"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/metric"
	"github.com/ccfos/huatuo/pkg/types"
)

type xfsCollector struct{}

func init() {
	tracing.RegisterEventTracing("xfs", newXFS)
}

// newXFS returns a new Collector exposing XFS filesystem runtime statistics.
// It is only registered on hosts where the XFS kernel interface is present.
func newXFS() (*tracing.EventTracingAttr, error) {
	if err := procfs.RequireFile("fs/xfs/stat"); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, types.ErrNotSupported
		}

		return nil, fmt.Errorf("check xfs statistics support: %w", err)
	}

	return &tracing.EventTracingAttr{
		TracingData: &xfsCollector{},
		Flag:        tracing.FlagMetric,
	}, nil
}

// Update reads per-filesystem XFS statistics from /sys/fs/xfs and exposes
// them as Prometheus metrics labeled by device.
func (c *xfsCollector) Update() ([]*metric.Data, error) {
	fsys, err := xfsproc.NewDefaultFS()
	if err != nil {
		return nil, err
	}

	stats, err := fsys.SysStats()
	if err != nil {
		return nil, err
	}

	data := make([]*metric.Data, 0, len(stats)*7)
	for _, s := range stats {
		data = append(data, xfsData(s)...)
	}

	if len(data) == 0 {
		return nil, metric.ErrNoData
	}

	return data, nil
}

// xfsData converts one filesystem's XFS statistics into metric data points.
func xfsData(s *xfsproc.Stats) []*metric.Data {
	labels := map[string]string{"device": s.Name}

	return []*metric.Data{
		metric.NewCounterData("alloc_blocks_total", float64(s.ExtentAllocation.BlocksAllocated), "Number of blocks allocated by extent allocation.", labels),
		metric.NewCounterData("alloc_extents_total", float64(s.ExtentAllocation.ExtentsAllocated), "Number of extents allocated.", labels),
		metric.NewCounterData("inode_attempts_total", float64(s.InodeOperation.Attempts), "Number of inode lookups attempted.", labels),
		metric.NewCounterData("inode_missed_total", float64(s.InodeOperation.Missed), "Number of inode lookups missed.", labels),
		metric.NewCounterData("buf_locked_waited_total", float64(s.Buffer.GetLockedWaited), "Number of times a buffer lock was waited for.", labels),
		metric.NewCounterData("buf_busy_locked_total", float64(s.Buffer.BusyLocked), "Number of times a busy buffer lock was encountered.", labels),
		metric.NewCounterData("log_space_sleep_total", float64(s.PushAil.SleepLogspace), "Number of times the AIL push slept waiting for log space.", labels),
	}
}
