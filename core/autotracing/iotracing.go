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
	"errors"
	"fmt"
	"os"
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

const (
	iotracingToolName                = "iotracing"
	iotracingSnapshotTimeout         = 5 * time.Second
	iotracingSnapshotSaveTimeout     = 30 * time.Second
	iotracingProcessWaitTimeout      = 5 * time.Second
	iotracingSamplingIntervalSeconds = 5
)

// pendingReasons correlates an inflight subprocess invocation with
// the core-side trigger reason that the subprocess cannot provide.
var pendingReasons sync.Map

type pendingIOTracingReason struct {
	reason   *reasonSnapshot
	received chan struct{}
	result   chan error
}

func init() {
	tracing.RegisterEventTracing(iotracingToolName, newIOTracing)
	toolstream.RegisterDefault[*types.IOTracingSnapshot](iotracingToolName, handleIotracingEvent)
}

func handleIotracingEvent(sess *toolstream.Session, ev *types.IOTracingSnapshot) error {
	var pending *pendingIOTracingReason
	if v, ok := pendingReasons.LoadAndDelete(sess.TaskID); ok {
		pending = v.(*pendingIOTracingReason)
		close(pending.received)
	}

	var reason *reasonSnapshot
	if pending != nil {
		reason = pending.reason
	}

	err := tracing.Save(&tracing.WriteRequest{
		TracerName: iotracingToolName,
		TracerTime: time.Now(),
		TracerData: &ioStatusData{
			Reason:      reason,
			Processes:   ev.Processes,
			StallStacks: ev.StallStacks,
		},
		TracerRunType: tracing.TracerRunTypeAutotracing,
	})
	if err != nil {
		err = fmt.Errorf("save iotracing snapshot: %w", err)
	}
	if pending != nil {
		pending.result <- err
	}

	return err
}

func newIOTracing() (*tracing.EventTracingAttr, error) {
	tracer, err := newIOTracer(cfg)
	if err != nil {
		return nil, err
	}

	return &tracing.EventTracingAttr{
		TracingData: tracer,
		Interval:    5,
		Flag:        tracing.FlagTracing,
	}, nil
}

type ioTracing struct {
	thresholds              ioThresholds
	samplingIntervalSeconds uint64
	runDurationSeconds      uint64
	maxProcesses            int
	maxFilesPerProcess      int
}

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/iotracing.c -o $BPF_DIR/iotracing.o

type ioStatusData struct {
	Reason      *reasonSnapshot            `json:"reason_snapshot"`
	Processes   []types.ProcessFileIOStats `json:"process_file_io_stats"`
	StallStacks []types.IOScheduleEvent    `json:"io_schedule_timeout_stacks"`
}

type diskStatus struct {
	ReadBPS    uint64 `json:"read_bps"`
	ReadIOPS   uint64 `json:"read_iops"`
	ReadAwait  uint64 `json:"read_await"`
	WriteBPS   uint64 `json:"write_bps"`
	WriteIOPS  uint64 `json:"write_iops"`
	WriteAwait uint64 `json:"write_await"`
	IOUtil     uint64 `json:"io_util"`
	QueueSize  uint64 `json:"queue_size"`
}

type reasonSnapshot struct {
	Type        string     `json:"type"`
	Device      string     `json:"device"`
	MajorNumber uint32     `json:"major_num"`
	MinorNumber uint32     `json:"minor_num"`
	IOStatus    diskStatus `json:"iostatus"`
	Summary     string     `json:"summary"`
}

type ioThresholds struct {
	RBPSThreshold  uint64
	WBPSThreshold  uint64
	UtilThreshold  uint64
	AwaitThreshold uint64
}

type thresholdReason string

const (
	ioReasonNone       thresholdReason = ""
	ioReasonUtil       thresholdReason = "ioutil"
	ioReasonReadBPS    thresholdReason = "read_bps"
	ioReasonWriteBPS   thresholdReason = "write_bps"
	ioReasonReadAwait  thresholdReason = "read_await"
	ioReasonWriteAwait thresholdReason = "write_await"
)

func thresholdReasonFor(
	previous diskStatus,
	current diskStatus,
	thresholds ioThresholds,
	isNVMe bool,
) thresholdReason {
	if previous.IOUtil > thresholds.UtilThreshold &&
		current.IOUtil > thresholds.UtilThreshold {
		if isNVMe {
			// https://man7.org/linux/man-pages/man1/iostat.1.html
			if previous.ReadBPS > thresholds.RBPSThreshold*1024*1024 &&
				current.ReadBPS > thresholds.RBPSThreshold*1024*1024 {
				return ioReasonReadBPS
			}
			if previous.WriteBPS > thresholds.WBPSThreshold*1024*1024 &&
				current.WriteBPS > thresholds.WBPSThreshold*1024*1024 {
				return ioReasonWriteBPS
			}
		} else {
			return ioReasonUtil
		}
	}

	if previous.ReadAwait > thresholds.AwaitThreshold &&
		current.ReadAwait > thresholds.AwaitThreshold {
		return ioReasonReadAwait
	}

	if previous.WriteAwait > thresholds.AwaitThreshold &&
		current.WriteAwait > thresholds.AwaitThreshold {
		return ioReasonWriteAwait
	}

	return ioReasonNone
}

func validateIOThresholds(thresholds ioThresholds) error {
	if thresholds.UtilThreshold == 0 {
		return fmt.Errorf(
			"io util threshold must be positive, got %d",
			thresholds.UtilThreshold,
		)
	}
	if thresholds.AwaitThreshold == 0 {
		return fmt.Errorf(
			"io await threshold must be positive, got %d",
			thresholds.AwaitThreshold,
		)
	}
	if thresholds.RBPSThreshold == 0 {
		return fmt.Errorf(
			"io read bps threshold must be positive, got %d",
			thresholds.RBPSThreshold,
		)
	}
	if thresholds.WBPSThreshold == 0 {
		return fmt.Errorf(
			"io write bps threshold must be positive, got %d",
			thresholds.WBPSThreshold,
		)
	}
	return nil
}

func readDiskStats() ([]blockdevice.Diskstats, error) {
	fs, err := blockdevice.NewDefaultFS()
	if err != nil {
		return nil, err
	}

	return fs.ProcDiskstats()
}

func buildDiskMetric(
	previous *blockdevice.Diskstats,
	current *blockdevice.Diskstats,
	intervalSeconds uint64,
) diskStatus {
	if intervalSeconds == 0 {
		return diskStatus{}
	}
	// Kernel counters reset when a device is removed and re-registered
	// under the same name (hotplug, driver rebind, LVM rebuild). Without
	// this guard the reset causes uint64 underflow in the delta below,
	// producing a fake metric that triggers a false IO alert.
	if current.ReadIOs < previous.ReadIOs || current.WriteIOs < previous.WriteIOs ||
		current.IOsTotalTicks < previous.IOsTotalTicks ||
		current.ReadSectors < previous.ReadSectors ||
		current.WriteSectors < previous.WriteSectors ||
		current.ReadTicks < previous.ReadTicks ||
		current.WriteTicks < previous.WriteTicks ||
		current.WeightedIOTicks < previous.WeightedIOTicks {
		return diskStatus{}
	}

	deltaReadIOs := current.ReadIOs - previous.ReadIOs
	deltaWriteIOs := current.WriteIOs - previous.WriteIOs

	metrics := diskStatus{
		IOUtil:    (current.IOsTotalTicks - previous.IOsTotalTicks) / (intervalSeconds * 10),
		QueueSize: (current.WeightedIOTicks - previous.WeightedIOTicks) / (intervalSeconds * 1000),
		ReadBPS:   ((current.ReadSectors - previous.ReadSectors) * 512) / intervalSeconds,
		WriteBPS:  ((current.WriteSectors - previous.WriteSectors) * 512) / intervalSeconds,
		ReadIOPS:  deltaReadIOs / intervalSeconds,
		WriteIOPS: deltaWriteIOs / intervalSeconds,
	}

	if deltaReadIOs > 0 {
		metrics.ReadAwait = (current.ReadTicks - previous.ReadTicks) / deltaReadIOs
	}
	if deltaWriteIOs > 0 {
		metrics.WriteAwait = (current.WriteTicks - previous.WriteTicks) / deltaWriteIOs
	}

	return metrics
}

func waitForDiskEvent(
	ctx context.Context,
	intervalSeconds uint64,
	thresholds ioThresholds,
) (*reasonSnapshot, error) {
	lastRawStats := make(map[string]*blockdevice.Diskstats)
	lastMetrics := make(map[string]diskStatus)
	ticker := time.NewTicker(time.Duration(int64(intervalSeconds)) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, types.ErrExitByCancelCtx
		case <-ticker.C:
			currentRawStats, err := readDiskStats()
			if err != nil {
				return nil, err
			}

			currentDevices := make(map[string]struct{}, len(currentRawStats))
			for i := range currentRawStats {
				current := &currentRawStats[i]

				if strings.HasPrefix(current.DeviceName, "md") {
					continue
				}
				currentDevices[current.DeviceName] = struct{}{}

				if previous, ok := lastRawStats[current.DeviceName]; ok {
					metric := buildDiskMetric(previous, current, intervalSeconds)

					log.WithField("device", current.DeviceName).
						WithField("io_util_percent", metric.IOUtil).
						WithField("queue_size", metric.QueueSize).
						WithField("read_kbps", metric.ReadBPS/1024).
						WithField("write_kbps", metric.WriteBPS/1024).
						WithField("read_iops", metric.ReadIOPS).
						WithField("write_iops", metric.WriteIOPS).
						WithField("read_await_ms", metric.ReadAwait).
						WithField("write_await_ms", metric.WriteAwait).
						Debug("sampled disk io metrics")

					reasonType := thresholdReasonFor(
						lastMetrics[current.DeviceName],
						metric,
						thresholds,
						strings.HasPrefix(current.DeviceName, "nvme"),
					)
					if reasonType != ioReasonNone {
						device := fmt.Sprintf(
							"%s(%d:%d)",
							current.DeviceName,
							current.MajorNumber,
							current.MinorNumber,
						)
						return &reasonSnapshot{
							Type:        string(reasonType),
							Device:      current.DeviceName,
							MajorNumber: current.MajorNumber,
							MinorNumber: current.MinorNumber,
							IOStatus:    metric,
							Summary: iotracingSummary(
								reasonType,
								device,
								metric,
								thresholds,
							),
						}, nil
					}

					lastMetrics[current.DeviceName] = metric
				}

				lastRawStats[current.DeviceName] = current
			}
			deleteMissingDiskState(lastRawStats, lastMetrics, currentDevices)
		}
	}
}

func deleteMissingDiskState(
	rawStats map[string]*blockdevice.Diskstats,
	metrics map[string]diskStatus,
	currentDevices map[string]struct{},
) {
	for device := range rawStats {
		if _, ok := currentDevices[device]; ok {
			continue
		}
		delete(rawStats, device)
		delete(metrics, device)
	}
}

func newIOTracer(config *Config) (*ioTracing, error) {
	thresholds := ioThresholds{
		RBPSThreshold:  config.IOTracing.RbpsThreshold,
		WBPSThreshold:  config.IOTracing.WbpsThreshold,
		UtilThreshold:  config.IOTracing.UtilThreshold,
		AwaitThreshold: config.IOTracing.AwaitThreshold,
	}
	if err := validateIOThresholds(thresholds); err != nil {
		return nil, err
	}
	if config.IOTracing.RunTracingToolTimeout == 0 {
		return nil, errors.New("io tracing duration must be positive")
	}
	if config.IOTracing.MaxProcDump <= 0 {
		return nil, fmt.Errorf(
			"io max process dump must be positive, got %d",
			config.IOTracing.MaxProcDump,
		)
	}
	if config.IOTracing.MaxFilesPerProcDump <= 0 {
		return nil, fmt.Errorf(
			"io max files per process dump must be positive, got %d",
			config.IOTracing.MaxFilesPerProcDump,
		)
	}

	return &ioTracing{
		thresholds:              thresholds,
		samplingIntervalSeconds: iotracingSamplingIntervalSeconds,
		runDurationSeconds:      config.IOTracing.RunTracingToolTimeout,
		maxProcesses:            config.IOTracing.MaxProcDump,
		maxFilesPerProcess:      config.IOTracing.MaxFilesPerProcDump,
	}, nil
}

// Start waits for a disk-burst trigger then runs the iotracing tool as a
// subprocess; results stream back via toolstream. The trigger reason is
// stashed under a generated task ID so handleIotracingEvent can attach it.
func (i *ioTracing) Start(ctx context.Context) error {
	reasonSnapshot, err := waitForDiskEvent(
		ctx,
		i.samplingIntervalSeconds,
		i.thresholds,
	)
	if err != nil {
		return err
	}

	log.WithField("reason_snapshot", reasonSnapshot).
		Debug("detected disk io event")

	taskID, err := tracing.AllocTaskID()
	if err != nil {
		return fmt.Errorf("allocate iotracing task id: %w", err)
	}

	pending := &pendingIOTracingReason{
		reason:   reasonSnapshot,
		received: make(chan struct{}),
		result:   make(chan error, 1),
	}
	pendingReasons.Store(taskID, pending)

	args := []string{
		"--bpf-path", path.Join(internalconfig.CoreBpfDir, "iotracing.o"),
		"--output-storage", toolstream.DefaultSockPath,
		"--task-id", taskID,
		"--duration", strconv.FormatUint(i.runDurationSeconds, 10),
		"--max-process", strconv.Itoa(i.maxProcesses),
		"--max-files-per-process", strconv.Itoa(i.maxFilesPerProcess),
	}

	cmd := exec.CommandContext(
		ctx,
		path.Join(internalconfig.CoreBinDir, iotracingToolName),
		args...,
	)
	if err := cmd.Start(); err != nil {
		pendingReasons.Delete(taskID)
		return fmt.Errorf("start iotracing: %w", err)
	}

	log.WithField("pid", cmd.Process.Pid).Info("iotracing started")

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case <-ctx.Done():
		pendingReasons.Delete(taskID)
		if err := killIOTracingProcessAndWait(
			cmd.Process,
			done,
			iotracingProcessWaitTimeout,
		); err != nil {
			return err
		}
		log.Info("iotracing stopped")
		return nil
	case werr := <-done:
		if ctx.Err() != nil {
			pendingReasons.Delete(taskID)
			log.Info("iotracing stopped")
			return nil
		}
		if werr != nil {
			pendingReasons.Delete(taskID)
			return fmt.Errorf("iotracing exited: %w", werr)
		}
	}

	return waitForSnapshot(
		ctx,
		taskID,
		pending,
		iotracingSnapshotTimeout,
		iotracingSnapshotSaveTimeout,
	)
}

func waitForSnapshot(
	ctx context.Context,
	taskID string,
	pending *pendingIOTracingReason,
	reportTimeout time.Duration,
	saveTimeout time.Duration,
) error {
	timer := time.NewTimer(reportTimeout)
	defer timer.Stop()

	select {
	case saveErr := <-pending.result:
		if saveErr != nil {
			return saveErr
		}
		log.Info("iotracing exited")
		return nil
	case <-pending.received:
		return waitForSnapshotSave(ctx, pending.result, saveTimeout)
	case <-ctx.Done():
		pendingReasons.Delete(taskID)
		return nil
	case <-timer.C:
		if _, loaded := pendingReasons.LoadAndDelete(taskID); loaded {
			return errors.New("iotracing exited without sending a snapshot")
		}
		return waitForSnapshotSave(ctx, pending.result, saveTimeout)
	}
}

func waitForSnapshotSave(
	ctx context.Context,
	result <-chan error,
	timeout time.Duration,
) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case saveErr := <-result:
		if saveErr != nil {
			return saveErr
		}
		log.Info("iotracing exited")
		return nil
	case <-ctx.Done():
		return nil
	case <-timer.C:
		return errors.New("timed out waiting for iotracing snapshot save")
	}
}

type processKiller interface {
	Kill() error
}

func killIOTracingProcessAndWait(
	process processKiller,
	done <-chan error,
	waitTimeout time.Duration,
) error {
	if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("stop iotracing: %w", err)
	}

	timer := time.NewTimer(waitTimeout)
	defer timer.Stop()

	select {
	case <-done:
		return nil
	case <-timer.C:
		return errors.New("timed out waiting for iotracing to stop")
	}
}

func iotracingSummary(
	reasonType thresholdReason,
	device string,
	ioStatus diskStatus,
	thresholds ioThresholds,
) string {
	switch reasonType {
	case ioReasonUtil:
		return fmt.Sprintf("ioutil=%d%% (threshold=%d%%) on %s, aqu-sz=%d, r_await=%dms w_await=%dms",
			ioStatus.IOUtil, thresholds.UtilThreshold, device, ioStatus.QueueSize,
			ioStatus.ReadAwait, ioStatus.WriteAwait)
	case ioReasonReadBPS:
		return fmt.Sprintf("read_bps=%dMB/s (threshold=%dMB/s) on %s, aqu-sz=%d, r_await=%dms w_await=%dms",
			ioStatus.ReadBPS/1024/1024, thresholds.RBPSThreshold, device, ioStatus.QueueSize,
			ioStatus.ReadAwait, ioStatus.WriteAwait)
	case ioReasonWriteBPS:
		return fmt.Sprintf("write_bps=%dMB/s (threshold=%dMB/s) on %s, aqu-sz=%d, r_await=%dms w_await=%dms",
			ioStatus.WriteBPS/1024/1024, thresholds.WBPSThreshold, device, ioStatus.QueueSize,
			ioStatus.ReadAwait, ioStatus.WriteAwait)
	case ioReasonReadAwait:
		return fmt.Sprintf("r_await=%dms (threshold=%dms) on %s, aqu-sz=%d",
			ioStatus.ReadAwait, thresholds.AwaitThreshold, device, ioStatus.QueueSize)
	case ioReasonWriteAwait:
		return fmt.Sprintf("w_await=%dms (threshold=%dms) on %s, aqu-sz=%d",
			ioStatus.WriteAwait, thresholds.AwaitThreshold, device, ioStatus.QueueSize)
	default:
		return fmt.Sprintf("%s on %s", reasonType, device)
	}
}
