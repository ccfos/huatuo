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

package autotracing

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"huatuo-bamai/internal/conf"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/procfs/blockdevice"
	"huatuo-bamai/internal/storage"
	"huatuo-bamai/internal/symbol"
	"huatuo-bamai/pkg/tracing"
	"huatuo-bamai/pkg/types"
)

func init() {
	tracing.RegisterEventTracing("iotracing", newIoTracing)
}

func newIoTracing() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &ioTracing{},
		Interval:    5,
		Flag:        tracing.FlagTracing,
	}, nil
}

type ioTracing struct{}

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/iotracing.c -o $BPF_DIR/iotracing.o

// IOStatusData contains IO status information.
type IOStatusData struct {
	Reason      *ReasonSnapshot `json:"reason"`
	ProcessData []ProcFileData  `json:"process_io_data"`
	IOStack     []IOStack       `json:"timeout_io_stack"`
}

// IOStack records io_schedule backtrace.
type IOStack struct {
	Pid               uint32       `json:"pid"`
	Comm              string       `json:"comm"`
	ContainerHostname string       `json:"container_hostname"`
	Latency           uint64       `json:"latency_us"`
	Stack             symbol.Stack `json:"stack"`
}

// ProcFileData records process information.
type ProcFileData struct {
	Pid               uint32   `json:"pid"`
	Comm              string   `json:"comm"`
	ContainerHostname string   `json:"container_hostname"`
	FsRead            uint64   `json:"fs_read"`
	FsWrite           uint64   `json:"fs_write"`
	DiskRead          uint64   `json:"disk_read"`
	DiskWrite         uint64   `json:"disk_write"`
	FileStat          []string `json:"file_stat"`
	FileCount         uint64   `json:"file_count"`
}

// DiskStatus represents calculated delta metrics
// Only includes currently used fields; extensible for more
type DiskStatus struct {
	ReadBps    uint64 `json:"read_bps"`
	ReadIOps   uint64 `json:"read_iops"`
	ReadAwait  uint64 `json:"read_await"`
	WriteBps   uint64 `json:"write_bps"`
	WriteIOps  uint64 `json:"write_iops"`
	WriteAwait uint64 `json:"write_await"`
	IOutil     uint64 `json:"io_util"`
	QueueSize  uint64 `json:"queue_size"`
	// Additional fields can be added as needed
}

type ReasonSnapshot struct {
	Type     string     `json:"type"`
	Device   string     `json:"device"`
	Iostatus DiskStatus `json:"iostatus"`
}

// IoThresholds holds threshold values independently
type IoThresholds struct {
	RbpsThreshold  uint64
	WbpsThreshold  uint64
	UtilThreshold  uint64
	AwaitThreshold uint64
	nvme           bool
}

type thresholdReason int

const (
	ioReasonNone thresholdReason = iota
	ioReasonUtil
	ioReasonReadBps
	ioReasonWriteBps
	ioReasonReadAwait
	ioReasonWriteAwait
)

func (threshold thresholdReason) String() string {
	switch threshold {
	case ioReasonNone:
		return "not_threshold"
	case ioReasonUtil:
		return "ioutil"
	case ioReasonReadBps:
		return "read_bps"
	case ioReasonWriteBps:
		return "write_bps"
	case ioReasonReadAwait:
		return "read_await"
	case ioReasonWriteAwait:
		return "write_await"
	default:
		return "unknown"
	}
}

func shouldIoThreshold(prev, curr DiskStatus, thresholds IoThresholds) thresholdReason {
	if prev.IOutil > thresholds.UtilThreshold &&
		curr.IOutil > thresholds.UtilThreshold {
		if thresholds.nvme {
			// https://man7.org/linux/man-pages/man1/iostat.1.html
			if prev.ReadBps > thresholds.RbpsThreshold*1024*1024 &&
				curr.ReadBps > thresholds.RbpsThreshold*1024*1024 {
				return ioReasonReadBps
			}
			if prev.WriteBps > thresholds.WbpsThreshold*1024*1024 &&
				curr.WriteBps > thresholds.WbpsThreshold*1024*1024 {
				return ioReasonWriteBps
			}
		} else {
			return ioReasonUtil
		}
	}

	if prev.ReadAwait > thresholds.AwaitThreshold &&
		curr.ReadAwait > thresholds.AwaitThreshold {
		return ioReasonReadAwait
	}

	if prev.WriteAwait > thresholds.AwaitThreshold &&
		curr.WriteAwait > thresholds.AwaitThreshold {
		return ioReasonWriteAwait
	}

	return ioReasonNone
}

func ReadDiskStats() ([]blockdevice.Diskstats, error) {
	fs, err := blockdevice.NewDefaultFS()
	if err != nil {
		return nil, err
	}

	return fs.ProcDiskstats()
}

// blockdevice.Diskstats is heavy (168 bytes); consider passing it by pointer
func buildDiskMetric(prev, curr *blockdevice.Diskstats, intervalSeconds uint64) DiskStatus {
	deltaReadIOs := curr.ReadIOs - prev.ReadIOs
	deltaWriteIOs := curr.WriteIOs - prev.WriteIOs

	metrics := DiskStatus{
		IOutil:    (curr.IOsTotalTicks - prev.IOsTotalTicks) / (intervalSeconds * 10),
		QueueSize: (curr.WeightedIOTicks - prev.WeightedIOTicks) / (intervalSeconds * 1000),
		ReadBps:   ((curr.ReadSectors - prev.ReadSectors) * 512) / intervalSeconds,
		WriteBps:  ((curr.WriteSectors - prev.WriteSectors) * 512) / intervalSeconds,
		ReadIOps:  deltaReadIOs / intervalSeconds,
		WriteIOps: deltaWriteIOs / intervalSeconds,
	}

	if deltaReadIOs > 0 {
		// milliseconds
		metrics.ReadAwait = (curr.ReadTicks - prev.ReadTicks) / deltaReadIOs
	}
	if deltaWriteIOs > 0 {
		metrics.WriteAwait = (curr.WriteTicks - prev.WriteTicks) / deltaWriteIOs
	}

	return metrics
}

func waittingDiskEvents(ctx context.Context, intervalSeconds uint64, thresholds IoThresholds) (*ReasonSnapshot, error) {
	lastRawStats := make(map[string]*blockdevice.Diskstats)
	lastMetrics := make(map[string]DiskStatus)
	ticker := time.NewTicker(time.Duration(int64(intervalSeconds)) * time.Second)

	for {
		select {
		case <-ctx.Done():
			return nil, types.ErrExitByCancelCtx
		case <-ticker.C:
			currentRawStats, err := ReadDiskStats()
			if err != nil {
				return nil, err
			}

			for i := range currentRawStats {
				// ignore each iteration copies 168 bytes
				curr := &currentRawStats[i]

				if strings.HasPrefix(curr.DeviceName, "md") {
					continue
				}

				if prev, ok := lastRawStats[curr.DeviceName]; ok {
					metric := buildDiskMetric(prev, curr, intervalSeconds)

					log.Debugf("%s ioutils: %d, avgqu-sz: %d, rkB/s: %d, wkB/s: %d, r/s: %d, w/s: %d, r_awaitt: %d, w_await: %d",
						curr.DeviceName, metric.IOutil, metric.QueueSize,
						metric.ReadBps/1024, metric.WriteBps/1024,
						metric.ReadIOps, metric.WriteIOps,
						metric.ReadAwait, metric.WriteAwait)

					thresholds.nvme = strings.HasPrefix(curr.DeviceName, "nvme")
					reasonType := shouldIoThreshold(lastMetrics[curr.DeviceName], metric, thresholds)
					if reasonType != ioReasonNone {
						return &ReasonSnapshot{
							Type:     reasonType.String(),
							Device:   curr.DeviceName,
							Iostatus: metric,
						}, nil
					}

					lastMetrics[curr.DeviceName] = metric
				}

				// store the pointers
				lastRawStats[curr.DeviceName] = curr
			}
		}
	}
}

// Start do the io tracer work
func (c *ioTracing) Start(ctx context.Context) error {
	thresholds := IoThresholds{
		RbpsThreshold:  conf.Get().AutoTracing.IOTracing.RbpsThreshold,
		WbpsThreshold:  conf.Get().AutoTracing.IOTracing.WbpsThreshold,
		UtilThreshold:  conf.Get().AutoTracing.IOTracing.UtilThreshold,
		AwaitThreshold: conf.Get().AutoTracing.IOTracing.AwaitThreshold,
	}

	reasonSnapshot, err := waittingDiskEvents(ctx, 5, thresholds)
	if err != nil {
		return err
	}

	log.Debugf("wait disk events with reason snapshot: %+v", reasonSnapshot)

	taskID := tracing.NewTask("iotracing", 40*time.Second, tracing.TaskStorageStdout, []string{"--json"})

	for {
		select {
		case <-ctx.Done():
			return types.ErrExitByCancelCtx
		case <-time.After(1 * time.Second):
			result := tracing.Result(taskID)

			log.Debugf("tracing tool result: %+v", result)

			switch result.TaskStatus {
			case tracing.StatusCompleted:
				if result.TaskErr != nil {
					return result.TaskErr
				}

				ioStatusData := IOStatusData{
					Reason: reasonSnapshot,
				}
				if err := json.Unmarshal(result.TaskData, &ioStatusData); err != nil {
					return fmt.Errorf("failed to unmarshal ioStatusData: %w", err)
				}

				storage.Save("iotracing", "", time.Now(), &ioStatusData)
				return nil
			case tracing.StatusFailed:
				return result.TaskErr
			}
		}
	}
}
