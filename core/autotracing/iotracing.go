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

package autotracing

import (
	"context"
	"fmt"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	internalconfig "huatuo-bamai/internal/config"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/procfs/blockdevice"
	"huatuo-bamai/internal/toolstream"
	"huatuo-bamai/pkg/tracing"
	"huatuo-bamai/pkg/types"
)

const iotracingToolName = "iotracing"

// pendingReasons correlates an inflight subprocess invocation (keyed by task ID)
// with the disk-event reason captured by the core. The handler attaches it to
// the saved record because the cmd has no concept of a trigger reason.
var pendingReasons sync.Map

func init() {
	tracing.RegisterEventTracing(iotracingToolName, newIoTracing)
	toolstream.RegisterDefault[*types.IOTracingReport](iotracingToolName, handleIotracingEvent)
}

func handleIotracingEvent(sess *toolstream.Session, ev *types.IOTracingReport) error {
	var reason *ReasonSnapshot
	if v, ok := pendingReasons.LoadAndDelete(sess.TaskID); ok {
		reason = v.(*ReasonSnapshot)
	}

	return tracing.Save(&tracing.WriteRequest{
		TracerName: iotracingToolName,
		TracerTime: time.Now(),
		TracerData: &IOStatusData{
			Reason:      reason,
			Processes:   ev.Processes,
			StallStacks: ev.StallStacks,
		},
		TracerRunType: tracing.TracerRunTypeAutotracing,
	})
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

// IOStatusData is the saved record: cmd-supplied process/stall data plus the
// core-side reason snapshot that triggered the trace.
type IOStatusData struct {
	Reason      *ReasonSnapshot            `json:"reason_snapshot"`
	Processes   []types.ProcessFileIOStats `json:"process_file_io_stats"`
	StallStacks []types.IOScheduleEvent    `json:"io_schedule_timeout_stacks"`
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
	Type        string     `json:"type"`
	Device      string     `json:"device"`
	MajorNumber uint32     `json:"major_num"`
	MinorNumber uint32     `json:"minor_num"`
	Iostatus    DiskStatus `json:"iostatus"`
	Summary     string     `json:"summary"`
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

func validateIoThresholds(c *IoThresholds) error {
	if c.UtilThreshold == 0 {
		return fmt.Errorf("io util threshold must be positive, got %d", c.UtilThreshold)
	}
	if c.AwaitThreshold == 0 {
		return fmt.Errorf("io await threshold must be positive, got %d", c.AwaitThreshold)
	}
	if c.RbpsThreshold == 0 {
		return fmt.Errorf("io read bps threshold must be positive, got %d", c.RbpsThreshold)
	}
	if c.WbpsThreshold == 0 {
		return fmt.Errorf("io write bps threshold must be positive, got %d", c.WbpsThreshold)
	}
	return nil
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
	if intervalSeconds == 0 {
		return DiskStatus{}
	}
	// Kernel counters reset when a device is removed and re-registered
	// under the same name (hotplug, driver rebind, LVM rebuild). Without
	// this guard the reset causes uint64 underflow in the delta below,
	// producing a fake metric that triggers a false IO alert.
	if curr.ReadIOs < prev.ReadIOs || curr.WriteIOs < prev.WriteIOs ||
		curr.IOsTotalTicks < prev.IOsTotalTicks {
		return DiskStatus{}
	}

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

func waitingDiskEvents(ctx context.Context, intervalSeconds uint64, thresholds IoThresholds) (*ReasonSnapshot, error) {
	lastRawStats := make(map[string]*blockdevice.Diskstats)
	lastMetrics := make(map[string]DiskStatus)
	ticker := time.NewTicker(time.Duration(int64(intervalSeconds)) * time.Second)
	defer ticker.Stop()

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
					device := fmt.Sprintf("%s(%d:%d)", curr.DeviceName, curr.MajorNumber, curr.MinorNumber)
					summary := iotracingSummary(reasonType, device, &metric, &thresholds)
					if reasonType != ioReasonNone {
						return &ReasonSnapshot{
							Type:        reasonType.String(),
							Device:      curr.DeviceName,
							MajorNumber: curr.MajorNumber,
							MinorNumber: curr.MinorNumber,
							Iostatus:    metric,
							Summary:     summary,
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

// Start waits for a disk-burst trigger then runs the iotracing tool as a
// subprocess; results stream back via toolstream. The trigger reason is
// stashed under a generated task ID so handleIotracingEvent can attach it.
func (c *ioTracing) Start(ctx context.Context) error {
	thresholds := IoThresholds{
		RbpsThreshold:  cfg.IOTracing.RbpsThreshold,
		WbpsThreshold:  cfg.IOTracing.WbpsThreshold,
		UtilThreshold:  cfg.IOTracing.UtilThreshold,
		AwaitThreshold: cfg.IOTracing.AwaitThreshold,
	}

	if err := validateIoThresholds(&thresholds); err != nil {
		return err
	}

	reasonSnapshot, err := waitingDiskEvents(ctx, 5, thresholds)
	if err != nil {
		return err
	}

	log.Debugf("wait disk events with reason snapshot: %+v", reasonSnapshot)

	duration := cfg.IOTracing.RunTracingToolTimeout
	taskID := fmt.Sprintf("iotracing-%d", time.Now().UnixNano())

	pendingReasons.Store(taskID, reasonSnapshot)
	defer pendingReasons.Delete(taskID)

	args := []string{
		"--bpf-path", path.Join(internalconfig.CoreBpfDir, "iotracing.o"),
		"--output-storage", toolstream.DefaultSockPath,
		"--task-id", taskID,
		"--duration", strconv.FormatUint(duration, 10),
	}

	cmd := exec.Command(path.Join(internalconfig.CoreBinDir, iotracingToolName), args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start iotracing: %w", err)
	}

	log.Infof("iotracing started pid=%d", cmd.Process.Pid)

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-done
		log.Info("iotracing stopped")
		return nil
	case werr := <-done:
		if werr != nil {
			return fmt.Errorf("iotracing exited: %w", werr)
		}
		log.Info("iotracing exited")
		return nil
	}
}

func iotracingSummary(reasonType thresholdReason, device string, iostat *DiskStatus, thresholds *IoThresholds) string {
	switch reasonType {
	case ioReasonUtil:
		return fmt.Sprintf("ioutil=%d%% (threshold=%d%%) on %s, aqu-sz=%d, r_await=%dms w_await=%dms",
			iostat.IOutil, thresholds.UtilThreshold, device, iostat.QueueSize,
			iostat.ReadAwait, iostat.WriteAwait)
	case ioReasonReadBps:
		return fmt.Sprintf("read_bps=%dMB/s (threshold=%dMB/s) on %s, aqu-sz=%d, r_await=%dms w_await=%dms",
			iostat.ReadBps/1024/1024, thresholds.RbpsThreshold, device, iostat.QueueSize,
			iostat.ReadAwait, iostat.WriteAwait)
	case ioReasonWriteBps:
		return fmt.Sprintf("write_bps=%dMB/s (threshold=%dMB/s) on %s, aqu-sz=%d, r_await=%dms w_await=%dms",
			iostat.WriteBps/1024/1024, thresholds.WbpsThreshold, device, iostat.QueueSize,
			iostat.ReadAwait, iostat.WriteAwait)
	case ioReasonReadAwait:
		return fmt.Sprintf("r_await=%dms (threshold=%dms) on %s, aqu-sz=%d",
			iostat.ReadAwait, thresholds.AwaitThreshold, device, iostat.QueueSize)
	case ioReasonWriteAwait:
		return fmt.Sprintf("w_await=%dms (threshold=%dms) on %s, aqu-sz=%d",
			iostat.WriteAwait, thresholds.AwaitThreshold, device, iostat.QueueSize)
	default:
		return fmt.Sprintf("%s on %s", reasonType, device)
	}
}
