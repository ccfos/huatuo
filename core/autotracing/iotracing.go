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
			Reason:        reason,
			FailureReason: ev.FailureReason,
			Processes:     ev.Processes,
			StallStacks:   ev.StallStacks,
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

type diskStatusSnapshot struct {
	devices map[string]diskStatus
	order   []string
}

type rawDiskstatsSnapshot struct {
	timestamp time.Time
	devices   map[string]blockdevice.Diskstats
	order     []string
}

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/iotracing.c -o $BPF_DIR/iotracing.o

type ioStatusData struct {
	Reason        *reasonSnapshot            `json:"reason_snapshot"`
	FailureReason string                     `json:"failure_reason,omitempty"`
	Processes     []types.ProcessFileIOStats `json:"process_file_io_stats"`
	StallStacks   []types.IOScheduleEvent    `json:"io_schedule_timeout_stacks"`
}

type diskStatus struct {
	ReadBPS    float64 `json:"read_bps"`
	ReadIOPS   float64 `json:"read_iops"`
	ReadAwait  float64 `json:"read_await"`
	WriteBPS   float64 `json:"write_bps"`
	WriteIOPS  float64 `json:"write_iops"`
	WriteAwait float64 `json:"write_await"`
	IOUtil     float64 `json:"io_util"`
	QueueSize  float64 `json:"queue_size"`
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
	if previous.IOUtil > float64(thresholds.UtilThreshold) &&
		current.IOUtil > float64(thresholds.UtilThreshold) {
		if isNVMe {
			// https://man7.org/linux/man-pages/man1/iostat.1.html
			if previous.ReadBPS > float64(thresholds.RBPSThreshold)*1024*1024 &&
				current.ReadBPS > float64(thresholds.RBPSThreshold)*1024*1024 {
				return ioReasonReadBPS
			}
			if previous.WriteBPS > float64(thresholds.WBPSThreshold)*1024*1024 &&
				current.WriteBPS > float64(thresholds.WBPSThreshold)*1024*1024 {
				return ioReasonWriteBPS
			}
		} else {
			return ioReasonUtil
		}
	}

	if previous.ReadAwait > float64(thresholds.AwaitThreshold) &&
		current.ReadAwait > float64(thresholds.AwaitThreshold) {
		return ioReasonReadAwait
	}

	if previous.WriteAwait > float64(thresholds.AwaitThreshold) &&
		current.WriteAwait > float64(thresholds.AwaitThreshold) {
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

func readRawDiskstatsSnapshot() (*rawDiskstatsSnapshot, error) {
	stats, err := readDiskStats()
	if err != nil {
		return nil, err
	}

	snapshot := &rawDiskstatsSnapshot{
		timestamp: time.Now(),
		devices:   make(map[string]blockdevice.Diskstats, len(stats)),
		order:     make([]string, 0, len(stats)),
	}
	for i := range stats {
		current := stats[i]
		snapshot.devices[current.DeviceName] = current
		snapshot.order = append(snapshot.order, current.DeviceName)
	}
	return snapshot, nil
}

func validDiskstatsWindow(
	previous *blockdevice.Diskstats,
	current *blockdevice.Diskstats,
) bool {
	if previous == nil || current == nil ||
		previous.MajorNumber != current.MajorNumber ||
		previous.MinorNumber != current.MinorNumber {
		return false
	}

	// Kernel counters reset when a device is removed and re-registered
	// under the same name (hotplug, driver rebind, LVM rebuild). Without
	// this guard the reset causes uint64 underflow in the delta below,
	// producing a fake metric that triggers a false IO alert.
	return current.ReadIOs >= previous.ReadIOs &&
		current.WriteIOs >= previous.WriteIOs &&
		current.IOsTotalTicks >= previous.IOsTotalTicks &&
		current.ReadSectors >= previous.ReadSectors &&
		current.WriteSectors >= previous.WriteSectors &&
		current.ReadTicks >= previous.ReadTicks &&
		current.WriteTicks >= previous.WriteTicks &&
		current.WeightedIOTicks >= previous.WeightedIOTicks
}

func buildDiskMetric(
	previous *blockdevice.Diskstats,
	current *blockdevice.Diskstats,
	elapsed time.Duration,
) (diskStatus, bool) {
	if elapsed <= 0 || !validDiskstatsWindow(previous, current) {
		return diskStatus{}, false
	}

	elapsedSeconds := elapsed.Seconds()
	readIOs := current.ReadIOs - previous.ReadIOs
	writeIOs := current.WriteIOs - previous.WriteIOs
	status := diskStatus{
		ReadBPS:   float64(current.ReadSectors-previous.ReadSectors) * 512 / elapsedSeconds,
		ReadIOPS:  float64(readIOs) / elapsedSeconds,
		WriteBPS:  float64(current.WriteSectors-previous.WriteSectors) * 512 / elapsedSeconds,
		WriteIOPS: float64(writeIOs) / elapsedSeconds,
		IOUtil:    float64(current.IOsTotalTicks-previous.IOsTotalTicks) / (elapsedSeconds * 1000) * 100,
		QueueSize: float64(current.WeightedIOTicks-previous.WeightedIOTicks) / (elapsedSeconds * 1000),
	}

	if readIOs > 0 {
		status.ReadAwait = float64(current.ReadTicks-previous.ReadTicks) / float64(readIOs)
	}
	if writeIOs > 0 {
		status.WriteAwait = float64(current.WriteTicks-previous.WriteTicks) / float64(writeIOs)
	}

	return status, true
}

func buildDiskStatusSnapshot(
	previous *rawDiskstatsSnapshot,
	current *rawDiskstatsSnapshot,
) *diskStatusSnapshot {
	snapshot := &diskStatusSnapshot{
		devices: make(map[string]diskStatus, len(current.devices)),
		order:   make([]string, 0, len(current.order)),
	}
	elapsed := current.timestamp.Sub(previous.timestamp)
	for _, name := range current.order {
		currentDisk := current.devices[name]
		previousDisk, ok := previous.devices[name]
		if !ok {
			continue
		}
		status, ok := buildDiskMetric(&previousDisk, &currentDisk, elapsed)
		if !ok {
			continue
		}
		snapshot.devices[name] = status
		snapshot.order = append(snapshot.order, name)
	}
	return snapshot
}

func evaluateThresholds(
	raw *rawDiskstatsSnapshot,
	snapshot *diskStatusSnapshot,
	lastMetrics map[string]diskStatus,
	thresholds ioThresholds,
) *reasonSnapshot {
	for name := range lastMetrics {
		if _, valid := snapshot.devices[name]; !valid {
			delete(lastMetrics, name)
		}
	}

	for _, name := range snapshot.order {
		if strings.HasPrefix(name, "md") {
			continue
		}

		status := snapshot.devices[name]
		log.WithField("device", name).
			WithField("io_util_percent", status.IOUtil).
			WithField("queue_size", status.QueueSize).
			WithField("read_kbps", status.ReadBPS/1024).
			WithField("write_kbps", status.WriteBPS/1024).
			WithField("read_iops", status.ReadIOPS).
			WithField("write_iops", status.WriteIOPS).
			WithField("read_await_ms", status.ReadAwait).
			WithField("write_await_ms", status.WriteAwait).
			Debug("sampled disk io metrics")

		reasonType := thresholdReasonFor(
			lastMetrics[name],
			status,
			thresholds,
			strings.HasPrefix(name, "nvme"),
		)
		if reasonType != ioReasonNone {
			device := raw.devices[name]
			deviceLabel := fmt.Sprintf(
				"%s(%d:%d)",
				name,
				device.MajorNumber,
				device.MinorNumber,
			)
			return &reasonSnapshot{
				Type:        string(reasonType),
				Device:      name,
				MajorNumber: device.MajorNumber,
				MinorNumber: device.MinorNumber,
				IOStatus:    status,
				Summary: iotracingSummary(
					reasonType,
					deviceLabel,
					status,
					thresholds,
				),
			}
		}

		lastMetrics[name] = status
	}
	return nil
}

func waitForDiskEvent(
	ctx context.Context,
	interval time.Duration,
	thresholds ioThresholds,
) (*reasonSnapshot, error) {
	var previous *rawDiskstatsSnapshot
	lastMetrics := make(map[string]diskStatus)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, types.ErrExitByCancelCtx
		case <-ticker.C:
			current, err := readRawDiskstatsSnapshot()
			if err != nil {
				return nil, err
			}
			if previous == nil {
				previous = current
				continue
			}

			snapshot := buildDiskStatusSnapshot(previous, current)
			reason := evaluateThresholds(
				current,
				snapshot,
				lastMetrics,
				thresholds,
			)
			if reason != nil {
				return reason, nil
			}
			previous = current
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
		time.Duration(i.samplingIntervalSeconds)*time.Second,
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

	var exitErr error
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
		exitErr = werr
	}

	return waitForSnapshotAfterExit(ctx, taskID, pending, exitErr)
}

func waitForSnapshotAfterExit(
	ctx context.Context,
	taskID string,
	pending *pendingIOTracingReason,
	exitErr error,
) error {
	snapshotErr := waitForSnapshot(
		ctx,
		taskID,
		pending,
		iotracingSnapshotTimeout,
		iotracingSnapshotSaveTimeout,
	)
	if exitErr == nil {
		return snapshotErr
	}
	return errors.Join(fmt.Errorf("iotracing exited: %w", exitErr), snapshotErr)
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
		return fmt.Sprintf("ioutil=%.2f%% (threshold=%d%%) on %s, aqu-sz=%.2f, r_await=%.2fms w_await=%.2fms",
			ioStatus.IOUtil, thresholds.UtilThreshold, device, ioStatus.QueueSize,
			ioStatus.ReadAwait, ioStatus.WriteAwait)
	case ioReasonReadBPS:
		return fmt.Sprintf("read_bps=%.2fMB/s (threshold=%dMB/s) on %s, aqu-sz=%.2f, r_await=%.2fms w_await=%.2fms",
			ioStatus.ReadBPS/1024/1024, thresholds.RBPSThreshold, device, ioStatus.QueueSize,
			ioStatus.ReadAwait, ioStatus.WriteAwait)
	case ioReasonWriteBPS:
		return fmt.Sprintf("write_bps=%.2fMB/s (threshold=%dMB/s) on %s, aqu-sz=%.2f, r_await=%.2fms w_await=%.2fms",
			ioStatus.WriteBPS/1024/1024, thresholds.WBPSThreshold, device, ioStatus.QueueSize,
			ioStatus.ReadAwait, ioStatus.WriteAwait)
	case ioReasonReadAwait:
		return fmt.Sprintf("r_await=%.2fms (threshold=%dms) on %s, aqu-sz=%.2f",
			ioStatus.ReadAwait, thresholds.AwaitThreshold, device, ioStatus.QueueSize)
	case ioReasonWriteAwait:
		return fmt.Sprintf("w_await=%.2fms (threshold=%dms) on %s, aqu-sz=%.2f",
			ioStatus.WriteAwait, thresholds.AwaitThreshold, device, ioStatus.QueueSize)
	default:
		return fmt.Sprintf("%s on %s", reasonType, device)
	}
}
