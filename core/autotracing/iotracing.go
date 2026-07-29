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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ccfos/huatuo/internal/timeutil"

	internalconfig "github.com/ccfos/huatuo/internal/config"
	"github.com/ccfos/huatuo/internal/executil"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/procfs/blockdevice"
	"github.com/ccfos/huatuo/internal/randomid"
	"github.com/ccfos/huatuo/internal/toolstream"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/types"
)

const (
	iotracingToolName                = "iotracing"
	iotracingSnapshotTimeout         = 5 * time.Second
	iotracingSnapshotSaveTimeout     = 30 * time.Second
	iotracingSamplingIntervalSeconds = 5
)

// pendingReasons correlates an inflight subprocess invocation with
// the core-side trigger reason that the subprocess cannot provide.
var pendingReasons sync.Map

type pendingIOTracingReason struct {
	reason           *reasonSnapshot
	startedTimestamp timeutil.Timestamp
	received         chan struct{}
	result           chan error
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

	if pending == nil {
		return errors.New("iotracing snapshot has no pending request")
	}
	reason := pending.reason

	err := tracing.Save(&tracing.WriteRequest{
		TracerName:       iotracingToolName,
		StartedTimestamp: pending.startedTimestamp,
		TracerData: &ioStatusData{
			Reason:        reason,
			FailureReason: ev.FailureReason,
			Processes:     ev.Processes,
			StallStacks:   ev.StallStacks,
		},
		TracerRunType: types.TracerRunTypeAutotracing,
	})
	if err != nil {
		err = fmt.Errorf("save iotracing snapshot: %w", err)
	}
	pending.result <- err

	return err
}

func newIOTracing() (*tracing.EventTracingAttr, error) {
	tracer, err := newIOTracer(configSnapshot())
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
	devices map[string]diskMetricStatus
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
	ReadBPS    uint64 `json:"read_bps"`
	ReadIOPS   uint64 `json:"read_iops"`
	ReadAwait  uint64 `json:"read_await"`
	WriteBPS   uint64 `json:"write_bps"`
	WriteIOPS  uint64 `json:"write_iops"`
	WriteAwait uint64 `json:"write_await"`
	IOUtil     uint64 `json:"io_util"`
	QueueSize  uint64 `json:"queue_size"`
}

// diskMetricStatus preserves fractional values for threshold and Prometheus
// calculations without changing the persisted diskStatus contract.
type diskMetricStatus struct {
	ReadBPS    float64
	ReadIOPS   float64
	ReadAwait  float64
	WriteBPS   float64
	WriteIOPS  float64
	WriteAwait float64
	IOUtil     float64
	QueueSize  float64
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
	previous diskMetricStatus,
	current diskMetricStatus,
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
		if !isMonitoredDisk(&current) {
			continue
		}
		snapshot.devices[current.DeviceName] = current
		snapshot.order = append(snapshot.order, current.DeviceName)
	}
	return snapshot, nil
}

func isMonitoredDisk(stat *blockdevice.Diskstats) bool {
	if stat == nil || stat.DeviceName == "" || isPseudoDisk(stat.DeviceName) {
		return false
	}

	devicePath := filepath.Join(
		procfs.DefaultPathByType("sys"),
		"dev",
		"block",
		fmt.Sprintf("%d:%d", stat.MajorNumber, stat.MinorNumber),
	)
	if _, err := os.Stat(devicePath); err != nil {
		return false
	}

	_, err := os.Stat(filepath.Join(devicePath, "partition"))
	if err == nil {
		return false
	}
	if !os.IsNotExist(err) {
		return false
	}

	// Confirm that a concurrent removal did not make the partition file
	// disappear before treating the entry as a whole device.
	_, err = os.Stat(devicePath)
	return err == nil
}

func isPseudoDisk(name string) bool {
	for _, prefix := range []string{"loop", "ram", "zram", "fd"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func validDiskstatsWindow(
	prev *blockdevice.Diskstats,
	curr *blockdevice.Diskstats,
) bool {
	if prev == nil || curr == nil ||
		prev.MajorNumber != curr.MajorNumber ||
		prev.MinorNumber != curr.MinorNumber {
		return false
	}

	// Kernel counters reset when a device is removed and re-registered
	// under the same name (hotplug, driver rebind, LVM rebuild). Without
	// this guard the reset causes uint64 underflow in the delta below,
	// producing a fake metric that triggers a false IO alert.
	return curr.ReadIOs >= prev.ReadIOs &&
		curr.WriteIOs >= prev.WriteIOs &&
		curr.IOsTotalTicks >= prev.IOsTotalTicks &&
		curr.ReadSectors >= prev.ReadSectors &&
		curr.WriteSectors >= prev.WriteSectors &&
		curr.ReadTicks >= prev.ReadTicks &&
		curr.WriteTicks >= prev.WriteTicks &&
		curr.WeightedIOTicks >= prev.WeightedIOTicks
}

// blockdevice.Diskstats is heavy (168 bytes); consider passing it by pointer
func buildDiskMetric(
	prev *blockdevice.Diskstats,
	curr *blockdevice.Diskstats,
	elapsed time.Duration,
) (diskMetricStatus, bool) {
	if elapsed <= 0 || !validDiskstatsWindow(prev, curr) {
		return diskMetricStatus{}, false
	}

	elapsedSeconds := elapsed.Seconds()
	readIOs := curr.ReadIOs - prev.ReadIOs
	writeIOs := curr.WriteIOs - prev.WriteIOs
	status := diskMetricStatus{
		ReadBPS:   float64(curr.ReadSectors-prev.ReadSectors) * 512 / elapsedSeconds,
		ReadIOPS:  float64(readIOs) / elapsedSeconds,
		WriteBPS:  float64(curr.WriteSectors-prev.WriteSectors) * 512 / elapsedSeconds,
		WriteIOPS: float64(writeIOs) / elapsedSeconds,
		IOUtil:    float64(curr.IOsTotalTicks-prev.IOsTotalTicks) / (elapsedSeconds * 1000) * 100,
		QueueSize: float64(curr.WeightedIOTicks-prev.WeightedIOTicks) / (elapsedSeconds * 1000),
	}

	if readIOs > 0 {
		// milliseconds
		status.ReadAwait = float64(curr.ReadTicks-prev.ReadTicks) / float64(readIOs)
	}
	if writeIOs > 0 {
		status.WriteAwait = float64(curr.WriteTicks-prev.WriteTicks) / float64(writeIOs)
	}

	return status, true
}

func buildDiskStatusSnapshot(
	previous *rawDiskstatsSnapshot,
	current *rawDiskstatsSnapshot,
) *diskStatusSnapshot {
	snapshot := &diskStatusSnapshot{
		devices: make(map[string]diskMetricStatus, len(current.devices)),
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
	lastMetrics map[string]diskMetricStatus,
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

		reasonType := thresholdReasonFor(lastMetrics[name], status, thresholds, strings.HasPrefix(name, "nvme"))
		if reasonType != ioReasonNone {
			device := raw.devices[name]
			persisted := persistedDiskStatus(status)
			return &reasonSnapshot{
				Type:        string(reasonType),
				Device:      device.DeviceName,
				MajorNumber: device.MajorNumber,
				MinorNumber: device.MinorNumber,
				IOStatus:    persisted,
				Summary: iotracingSummary(reasonType,
					fmt.Sprintf("%s(%d:%d)", device.DeviceName, device.MajorNumber, device.MinorNumber),
					persisted, thresholds),
			}
		}

		lastMetrics[name] = status
	}
	return nil
}

func persistedDiskStatus(status diskMetricStatus) diskStatus {
	return diskStatus{
		ReadBPS:    uint64(status.ReadBPS),
		ReadIOPS:   uint64(status.ReadIOPS),
		ReadAwait:  uint64(status.ReadAwait),
		WriteBPS:   uint64(status.WriteBPS),
		WriteIOPS:  uint64(status.WriteIOPS),
		WriteAwait: uint64(status.WriteAwait),
		IOUtil:     uint64(status.IOUtil),
		QueueSize:  uint64(status.QueueSize),
	}
}

func waitForDiskEvent(ctx context.Context, intervalSeconds uint64, thresholds ioThresholds) (*reasonSnapshot, error) {
	var previous *rawDiskstatsSnapshot
	lastMetrics := make(map[string]diskMetricStatus)
	ticker := time.NewTicker(time.Duration(intervalSeconds) * time.Second)
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

	taskID, err := randomid.New()
	if err != nil {
		return fmt.Errorf("allocate iotracing task id: %w", err)
	}

	pending := &pendingIOTracingReason{
		reason:           reasonSnapshot,
		startedTimestamp: timeutil.Now(),
		received:         make(chan struct{}),
		result:           make(chan error, 1),
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

	process, err := executil.New(executil.Spec{
		Path: path.Join(internalconfig.CoreBinDir, iotracingToolName),
		Args: args,
	})
	if err != nil {
		pendingReasons.Delete(taskID)
		return fmt.Errorf("build iotracing command: %w", err)
	}
	runErr := process.Run(ctx)
	_, outputErr := process.Stdout()
	processErr := errors.Join(runErr, outputErr)
	if err := processErr; err != nil {
		if errors.Is(err, executil.ErrStopFailed) {
			pendingReasons.Delete(taskID)
			stopErr := process.Stop(ctx)
			if stopErr == nil {
				log.Info("iotracing stopped")
				return nil
			}
			err = errors.Join(err, fmt.Errorf("retry stop iotracing: %w", stopErr))
			if stderr := process.Stderr(); len(stderr) > 0 {
				return fmt.Errorf("run iotracing: %w; stderr: %s", err, stderr)
			}
			return fmt.Errorf("run iotracing: %w", err)
		} else if ctx.Err() != nil {
			pendingReasons.Delete(taskID)
			log.Info("iotracing stopped")
			return nil
		}
		if stderr := process.Stderr(); len(stderr) > 0 {
			processErr = fmt.Errorf("run iotracing: %w; stderr: %s", err, stderr)
		} else {
			processErr = fmt.Errorf("run iotracing: %w", err)
		}
		// A failed launch has no snapshot. A child exit can still have sent
		// partial output which arrives after the process is reaped.
		var exitErr *exec.ExitError
		if runErr != nil && !errors.As(runErr, &exitErr) {
			pendingReasons.Delete(taskID)
			return processErr
		}
	}

	return waitForSnapshotAfterExit(
		ctx,
		taskID,
		pending,
		processErr,
		iotracingSnapshotTimeout,
		iotracingSnapshotSaveTimeout,
	)
}

func waitForSnapshotAfterExit(
	ctx context.Context,
	taskID string,
	pending *pendingIOTracingReason,
	exitErr error,
	reportTimeout time.Duration,
	saveTimeout time.Duration,
) error {
	snapshotErr := waitForSnapshot(ctx, taskID, pending, reportTimeout, saveTimeout)
	if ctx.Err() != nil {
		return nil
	}
	return errors.Join(exitErr, snapshotErr)
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
