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
	"fmt"
	"sync"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/internal/procfs/blockdevice"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

const sectorSize = 512

// diskDeviceStats holds the raw counters from /proc/diskstats for a single device,
// along with the timestamp of the last collection used for delta-based latency calculations.
type diskDeviceStats struct {
	readIOs    uint64
	writeIOs   uint64
	readTicks  uint64 // ms spent reading
	writeTicks uint64 // ms spent writing

	lastUpdate time.Time
}

type diskIOCollector struct {
	mu     sync.Mutex
	prev   map[string]*diskDeviceStats
	devFS  blockdevice.FS
	procFS procfs.FS
}

func init() {
	tracing.RegisterEventTracing("disk_io", newDiskIO)
}

func newDiskIO() (*tracing.EventTracingAttr, error) {
	devFS, err := blockdevice.NewDefaultFS()
	if err != nil {
		return nil, fmt.Errorf("disk_io: init blockdevice fs: %w", err)
	}

	procFS, err := procfs.NewDefaultFS()
	if err != nil {
		return nil, fmt.Errorf("disk_io: init procfs: %w", err)
	}

	return &tracing.EventTracingAttr{
		TracingData: &diskIOCollector{
			prev:   make(map[string]*diskDeviceStats),
			devFS:  devFS,
			procFS: procFS,
		},
		Flag: tracing.FlagMetric,
	}, nil
}

func (c *diskIOCollector) Update() ([]*metric.Data, error) {
	var metrics []*metric.Data

	// Collect per-device disk IO metrics from /proc/diskstats.
	deviceMetrics, err := c.collectDiskstats()
	if err != nil {
		log.Infof("disk_io: collect diskstats: %v", err)
	} else {
		metrics = append(metrics, deviceMetrics...)
	}

	// Collect system-wide iowait from /proc/stat.
	iowaitMetrics, err := c.collectIOWait()
	if err != nil {
		log.Infof("disk_io: collect iowait: %v", err)
	} else {
		metrics = append(metrics, iowaitMetrics...)
	}

	if len(metrics) == 0 {
		return nil, metric.ErrNoData
	}

	return metrics, nil
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
//	Field  7: time spent reading in milliseconds (cumulative counter, internal only)
//	Field  8: writes completed successfully (cumulative counter)
//	Field  9: writes merged — not used
//	Field 10: sectors written (each sector = 512 bytes)
//	Field 11: time spent writing in milliseconds (cumulative counter, internal only)
//	Field 12: I/Os currently in progress (gauge, only field that can decrease)
//	Field 13: time spent doing I/Os in ms (only counts when field 12 > 0) — not used
//	Field 14: weighted time spent doing I/Os (for backlog measurement) — not used
//
// # Exposed Metrics
//
// ## Counter Metrics (cumulative; use Prometheus rate() for per-second values)
//
//   - disk_read_requests_total (Counter):
//     Source: field 4. Cumulative count of read requests completed.
//     Use rate() in Prometheus to get read IOPS.
//
//   - disk_write_requests_total (Counter):
//     Source: field 8. Cumulative count of write requests completed.
//     Use rate() in Prometheus to get write IOPS.
//
//   - disk_read_bytes_total (Counter):
//     Source: field 6 × 512 (sector size). Cumulative bytes read.
//     Use rate() in Prometheus to get read throughput.
//
//   - disk_written_bytes_total (Counter):
//     Source: field 10 × 512 (sector size). Cumulative bytes written.
//     Use rate() in Prometheus to get write throughput.
//
// ## Gauge Metrics (point-in-time values)
//
//   - disk_io_in_progress (Gauge):
//     Source: field 12. Current number of I/O requests in flight (queue depth).
//
//   - disk_read_latency_ms (Gauge):
//     Computed as delta(field 7) / delta(field 4) between collection intervals.
//     Fields 7 and 4 are read internally but not exposed as separate metrics,
//     since they are only meaningful when combined to produce average latency.
//     Only emitted when delta(field 4) > 0 and delta(field 7) > 0.
//
//   - disk_write_latency_ms (Gauge):
//     Computed as delta(field 11) / delta(field 8) between collection intervals.
//     Only emitted when delta(field 8) > 0 and delta(field 11) > 0.
//
//   - disk_iowait_ratio (Gauge):
//     Source: /proc/stat cpu line (system-wide, not per-device).
//     Computed as: iowait_ticks / total_ticks × 100.
//
// # Counter Reset Handling
//
// All counters reset at boot, device reattachment, or counter overflow.
// The collector detects resets by checking if current < previous.
// When a reset is detected, delta-based metrics (latency) are skipped for that interval.
//
// # Device Compatibility
//
// Physical disks (sda, nvme0n1) have full IO statistics since kernel 2.6.x.
// For md (software RAID) devices, IO accounting was added in v5.14-rc1:
//   - raid0/raid5: commit 10764815ff47 ("md: add io accounting for raid0 and raid5")
//   - raid1: commit a0159832e51e ("md/raid1: enable io accounting")
//
// On kernels older than 5.14, md devices may appear in /proc/diskstats but
// fields 7/11 (read/write ticks) may be zero, resulting in no latency metrics.
// dm-* (device-mapper) devices generally have IO statistics available.
func (c *diskIOCollector) collectDiskstats() ([]*metric.Data, error) {
	diskstats, err := c.devFS.ProcDiskstats()
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	var metrics []*metric.Data
	now := time.Now()

	for _, ds := range diskstats {
		device := ds.DeviceName

		deviceLabel := map[string]string{"device": device}

		// Cumulative counters — use Prometheus rate() for per-second values.
		metrics = append(metrics,
			metric.NewCounterData("disk_read_requests_total", float64(ds.ReadIOs),
				"Total number of read requests completed successfully.", deviceLabel),
			metric.NewCounterData("disk_write_requests_total", float64(ds.WriteIOs),
				"Total number of write requests completed successfully.", deviceLabel),
			metric.NewCounterData("disk_read_bytes_total", float64(ds.ReadSectors)*sectorSize,
				"Total number of bytes read from the device.", deviceLabel),
			metric.NewCounterData("disk_written_bytes_total", float64(ds.WriteSectors)*sectorSize,
				"Total number of bytes written to the device.", deviceLabel),
		)

		// Gauge: current queue depth.
		metrics = append(metrics,
			metric.NewGaugeData("disk_io_in_progress", float64(ds.IOsInProgress),
				"Number of I/O requests currently in flight (queue depth).", deviceLabel),
		)

		// Gauge: average read/write latency computed from delta counters.
		prev, ok := c.prev[device]
		if ok {
			elapsed := now.Sub(prev.lastUpdate).Seconds()
			if elapsed > 0 {
				// Guard against counter reset (hot-unplug, reboot, wrap-around):
				// if the new value is less than the previous, skip delta computation.
				if ds.ReadIOs >= prev.readIOs && ds.ReadTicks >= prev.readTicks {
					deltaReadIOs := ds.ReadIOs - prev.readIOs
					deltaReadTicks := ds.ReadTicks - prev.readTicks
					// Skip when no IOs or ticks are zero (device may not support IO accounting).
					if deltaReadIOs > 0 && deltaReadTicks > 0 {
						avgReadLatency := float64(deltaReadTicks) / float64(deltaReadIOs)
						metrics = append(metrics,
							metric.NewGaugeData("disk_read_latency_ms", avgReadLatency,
								"Average read request latency in milliseconds.", deviceLabel),
						)
					}
				}

				if ds.WriteIOs >= prev.writeIOs && ds.WriteTicks >= prev.writeTicks {
					deltaWriteIOs := ds.WriteIOs - prev.writeIOs
					deltaWriteTicks := ds.WriteTicks - prev.writeTicks
					// Skip when no IOs or ticks are zero (device may not support IO accounting).
					if deltaWriteIOs > 0 && deltaWriteTicks > 0 {
						avgWriteLatency := float64(deltaWriteTicks) / float64(deltaWriteIOs)
						metrics = append(metrics,
							metric.NewGaugeData("disk_write_latency_ms", avgWriteLatency,
								"Average write request latency in milliseconds.", deviceLabel),
						)
					}
				}
			}
		}

		// Update cached previous values for delta-based latency computation.
		c.prev[device] = &diskDeviceStats{
			readIOs:    ds.ReadIOs,
			writeIOs:   ds.WriteIOs,
			readTicks:  ds.ReadTicks,
			writeTicks: ds.WriteTicks,
			lastUpdate: now,
		}
	}

	return metrics, nil
}

// collectIOWait reads /proc/stat and returns the system-wide iowait ratio.
func (c *diskIOCollector) collectIOWait() ([]*metric.Data, error) {
	stat, err := c.procFS.Stat()
	if err != nil {
		return nil, err
	}

	cpu := stat.CPUTotal
	total := cpu.User + cpu.Nice + cpu.System + cpu.Idle + cpu.Iowait + cpu.IRQ + cpu.SoftIRQ + cpu.Steal
	if total == 0 {
		return nil, nil
	}

	iowaitRatio := cpu.Iowait / total * 100

	return []*metric.Data{
		metric.NewGaugeData("disk_iowait_ratio", iowaitRatio,
			"Percentage of CPU time spent waiting for I/O operations to complete.", nil),
	}, nil
}
