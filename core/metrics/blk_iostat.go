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
	"sync"

	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/procfs/blockdevice"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/metric"
)

const (
	sectorSize = 512

	blkIOStatReadRequestsMetric  = "read_requests_total"
	blkIOStatWriteRequestsMetric = "write_requests_total"
	blkIOStatReadBytesMetric     = "read_bytes_total"
	blkIOStatWrittenBytesMetric  = "written_bytes_total"
	blkIOStatReadTimeMetric      = "read_time_seconds_total"
	blkIOStatWriteTimeMetric     = "write_time_seconds_total"
	blkIOStatInProgressMetric    = "io_in_progress"
	blkIOStatIOWaitPercentMetric = "disk_iowait_percent"
)

type cpuIOWaitSample struct {
	iowait float64
	total  float64
}

type blkIOStatCollector struct {
	mu      sync.Mutex
	prevCPU cpuIOWaitSample
	devFS   blockdevice.DiskstatsFS
	procFS  procfs.FS
}

func init() {
	tracing.RegisterEventTracing("blk_iostat", newBlkIOStat)
}

func newBlkIOStat() (*tracing.EventTracingAttr, error) {
	devFS, err := blockdevice.NewDiskstatsFS()
	if err != nil {
		return nil, fmt.Errorf("blk_iostat: init blockdevice fs: %w", err)
	}

	procFS, err := procfs.NewDefaultFS()
	if err != nil {
		return nil, fmt.Errorf("blk_iostat: init procfs: %w", err)
	}

	return &tracing.EventTracingAttr{
		TracingData: &blkIOStatCollector{
			devFS:  devFS,
			procFS: procFS,
		},
		Flag: tracing.FlagMetric,
	}, nil
}

func (c *blkIOStatCollector) Update() ([]*metric.Data, error) {
	metrics := make([]*metric.Data, 0)
	var diskstatsErr, iowaitErr error

	deviceMetrics, err := c.collectDiskstats()
	if err != nil {
		diskstatsErr = fmt.Errorf("collect diskstats: %w", err)
	} else {
		metrics = append(metrics, deviceMetrics...)
	}

	iowaitMetrics, err := c.collectIOWait()
	if err != nil {
		iowaitErr = fmt.Errorf("collect iowait: %w", err)
	} else {
		metrics = append(metrics, iowaitMetrics...)
	}

	collectionErr := errors.Join(diskstatsErr, iowaitErr)
	if len(metrics) == 0 {
		if collectionErr != nil {
			return nil, collectionErr
		}
		return nil, metric.ErrNoData
	}

	return metrics, collectionErr
}

// collectDiskstats reads /proc/diskstats and produces per-device metrics.
//
// # /proc/diskstats Format
//
// Each line contains 14+ fields separated by spaces. Example:
//
//	8       0 sda 1000 200 50000 3000 2000 400 80000 6000 50 9000 15000
//
// Field descriptions:
//
//	Field  1: major number (device type)
//	Field  2: minor number (device instance)
//	Field  3: device name (e.g., sda, nvme0n1, dm-0, md0)
//	Field  4: reads completed successfully (cumulative counter)
//	Field  5: reads merged (adjacent reads merged into one I/O) — not used
//	Field  6: sectors read (each sector = 512 bytes)
//	Field  7: time spent reading in milliseconds (cumulative counter)
//	Field  8: writes completed successfully (cumulative counter)
//	Field  9: writes merged — not used
//	Field 10: sectors written (each sector = 512 bytes)
//	Field 11: time spent writing in milliseconds (cumulative counter)
//	Field 12: I/Os currently in progress (gauge, only field that can decrease)
//	Field 13: time spent doing I/Os in ms (only counts when field 12 > 0) — not used
//	Field 14: weighted time spent doing I/Os (for backlog measurement) — not used
//
// # Exposed Metrics
//
// ## Counter Metrics (cumulative; use Prometheus rate() for per-second values)
//
//   - read_requests_total (Counter):
//     Source: field 4. Cumulative count of read requests completed.
//     Use rate() in Prometheus to get read IOPS.
//
//   - write_requests_total (Counter):
//     Source: field 8. Cumulative count of write requests completed.
//     Use rate() in Prometheus to get write IOPS.
//
//   - read_bytes_total (Counter):
//     Source: field 6 × 512 (sector size). Cumulative bytes read.
//     Use rate() in Prometheus to get read throughput.
//
//   - written_bytes_total (Counter):
//     Source: field 10 × 512 (sector size). Cumulative bytes written.
//     Use rate() in Prometheus to get write throughput.
//
//   - read_time_seconds_total (Counter):
//     Source: field 7. Cumulative seconds spent by completed reads.
//     Divide rate() of this metric by read request rate() for average latency.
//
//   - write_time_seconds_total (Counter):
//     Source: field 11. Cumulative seconds spent by completed writes.
//     Divide rate() of this metric by write request rate() for average latency.
//
// ## Gauge Metrics (point-in-time values)
//
//   - io_in_progress (Gauge):
//     Source: field 12. Current number of I/O requests in flight (queue depth).
//
//   - disk_iowait_percent (Gauge):
//     Source: /proc/stat cpu line (system-wide, not per-device).
//     Computed from the iowait and total CPU time deltas between collections.
//
// # Device Compatibility
//
// Physical disks (sda, nvme0n1) have full IO statistics since kernel 2.6.x.
// For md (software RAID) devices, IO accounting was added in v5.14-rc1:
//   - raid0/raid5: commit 10764815ff47 ("md: add io accounting for raid0 and raid5")
//   - raid1: commit a0159832e51e ("md/raid1: enable io accounting")
//
// On kernels older than 5.14, md devices may appear in /proc/diskstats but
// fields 7/11 (read/write ticks) may remain zero.
// dm-* (device-mapper) devices generally have IO statistics available.
func (c *blkIOStatCollector) collectDiskstats() ([]*metric.Data, error) {
	diskstats, err := c.devFS.ProcDiskstats()
	if err != nil {
		return nil, err
	}

	metrics := make([]*metric.Data, 0, len(diskstats)*7)

	for i := range diskstats {
		ds := &diskstats[i]
		device := ds.DeviceName

		deviceLabel := map[string]string{"disk": device}

		metrics = append(
			metrics,
			metric.NewCounterData(blkIOStatReadRequestsMetric, float64(ds.ReadIOs),
				"Total number of read requests completed successfully.", deviceLabel),
			metric.NewCounterData(blkIOStatWriteRequestsMetric, float64(ds.WriteIOs),
				"Total number of write requests completed successfully.", deviceLabel),
			metric.NewCounterData(blkIOStatReadBytesMetric, float64(ds.ReadSectors)*sectorSize,
				"Total number of bytes read from the device.", deviceLabel),
			metric.NewCounterData(blkIOStatWrittenBytesMetric, float64(ds.WriteSectors)*sectorSize,
				"Total number of bytes written to the device.", deviceLabel),
			metric.NewCounterData(blkIOStatReadTimeMetric, float64(ds.ReadTicks)/1000,
				"Total seconds spent by completed read requests.", deviceLabel),
			metric.NewCounterData(blkIOStatWriteTimeMetric, float64(ds.WriteTicks)/1000,
				"Total seconds spent by completed write requests.", deviceLabel),
			metric.NewGaugeData(blkIOStatInProgressMetric, float64(ds.IOsInProgress),
				"Number of I/O requests currently in flight (queue depth).", deviceLabel),
		)
	}

	return metrics, nil
}

// collectIOWait returns iowait over the interval between successful reads.
func (c *blkIOStatCollector) collectIOWait() ([]*metric.Data, error) {
	stat, err := c.procFS.Stat()
	if err != nil {
		return nil, err
	}

	cpu := stat.CPUTotal
	current := cpuIOWaitSample{
		iowait: cpu.Iowait,
		total: cpu.User + cpu.Nice + cpu.System + cpu.Idle +
			cpu.Iowait + cpu.IRQ + cpu.SoftIRQ + cpu.Steal,
	}
	if current.total == 0 {
		return nil, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	previous := c.prevCPU
	c.prevCPU = current
	if previous.total == 0 {
		return nil, nil
	}

	deltaTotal := current.total - previous.total
	deltaIOWait := current.iowait - previous.iowait
	if deltaTotal <= 0 || deltaIOWait < 0 || deltaIOWait > deltaTotal {
		return nil, nil
	}

	iowaitPercent := deltaIOWait / deltaTotal * 100

	return []*metric.Data{
		metric.NewGaugeData(blkIOStatIOWaitPercentMetric, iowaitPercent,
			"CPU time spent waiting for I/O during the collection interval.", nil),
	}, nil
}
