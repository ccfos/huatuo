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
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/paths"
	cgroupStats "github.com/ccfos/huatuo/internal/cgroups/stats"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	cgroupV2 "github.com/ccfos/huatuo/internal/cgroups/v2"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/matcher"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/types"

	cadvisorV1 "github.com/google/cadvisor/info/v1"
	"github.com/google/cadvisor/utils/cpuload/netlink"
	"github.com/prometheus/procfs"
	"github.com/shirou/gopsutil/process"
)

func init() {
	tracing.RegisterEventTracing("dload", newDload)
}

func newDload() (*tracing.EventTracingAttr, error) {
	config := configSnapshot()

	tracer, err := newDloadTracing(config)
	if err != nil {
		return nil, err
	}

	return &tracing.EventTracingAttr{
		TracingData: tracer,
		Interval:    30,
		Flag:        tracing.FlagTracing,
	}, nil
}

type containerDloadInfo struct {
	cpuPath     string
	cgroupName  string
	container   *pod.Container
	runnableAvg [2]uint64
	loadAvg     [2]float64
	dLoadAvg    [2]uint64
	dLoad       [2]float64
	isSeen      bool
	lastTraceAt time.Time
}

// DloadTracingData contains the load and task stacks captured by dload tracing.
type DloadTracingData struct {
	Threshold         uint64  `json:"threshold"`
	NrSleeping        uint64  `json:"nr_sleeping"`
	NrRunning         uint64  `json:"nr_running"`
	NrStopped         uint64  `json:"nr_stopped"`
	NrUninterruptible uint64  `json:"nr_uninterruptible"`
	NrIoWait          uint64  `json:"nr_iowait"`
	LoadAvg           float64 `json:"load_avg"`
	DLoadAvg          float64 `json:"dload_avg"`
	KnownIssue        string  `json:"known_issue"`
	Stack             string  `json:"stack"`
}

const (
	taskScopeHost taskScope = iota + 1
	taskScopeCgroup
)

type taskScope int

type dloadTracing struct {
	containers      map[string]*containerDloadInfo
	interval        time.Duration
	decayFactors    [2]uint64
	threshold       dloadThreshold
	v2LoadStats     func([]string) (map[string]cgroupStats.LoadStats, error)
	hostUnsupported bool
	host            containerDloadInfo
	hostThreshold   dloadThreshold
	hostStats       *cgroupStats.LoadStats
}

type dloadThreshold struct {
	load             int64
	minTraceInterval time.Duration
	isDebug          bool
}

func newDloadTracing(config *Config) (*dloadTracing, error) {
	if config.Dload.Interval <= 0 {
		return nil, errors.New("dload sampling interval must be positive")
	}
	if config.Dload.Interval > maxDurationSeconds {
		return nil, fmt.Errorf("dload sampling interval must not exceed %d seconds",
			maxDurationSeconds)
	}
	if config.Dload.IntervalTracing < 0 {
		return nil, errors.New("dload tracing interval must be non-negative")
	}
	if config.Dload.IntervalTracing > maxDurationSeconds {
		return nil, fmt.Errorf("dload tracing interval must not exceed %d seconds",
			maxDurationSeconds)
	}
	if config.Dload.ThresholdLoad < 0 {
		return nil, errors.New("dload threshold must be non-negative")
	}
	if err := config.validateDloadHostThreshold(); err != nil {
		return nil, err
	}

	interval := time.Duration(config.Dload.Interval) * time.Second
	d := &dloadTracing{
		containers:   make(map[string]*containerDloadInfo),
		interval:     interval,
		decayFactors: loadDecayFactors(interval),
		hostThreshold: dloadThreshold{
			load:             config.Dload.HostThresholdLoad,
			minTraceInterval: time.Duration(config.Dload.IntervalTracing) * time.Second,
			isDebug:          config.Dload.EnableDebug,
		},
		threshold: dloadThreshold{
			load:             config.Dload.ThresholdLoad,
			minTraceInterval: time.Duration(config.Dload.IntervalTracing) * time.Second,
			isDebug:          config.Dload.EnableDebug,
		},
	}
	d.v2LoadStats = func(paths []string) (map[string]cgroupStats.LoadStats, error) {
		result, host, err := cgroupV2.SharedLoadStatsWithHost(cgroupV2.LoadStatsConsumerDload, paths)
		d.hostStats = host
		return result, err
	}
	return d, nil
}

func (d *dloadTracing) reconcileContainers(containers map[string]*pod.Container) {
	for _, info := range d.containers {
		info.isSeen = false
	}
	for _, container := range containers {
		info, ok := d.containers[container.ID]
		if ok {
			info.cgroupName = container.CgroupPath
			info.cpuPath = paths.Path(subsystem.SubsystemCPU, container.CgroupPath)
			info.container = container
			info.isSeen = true
			continue
		}

		d.containers[container.ID] = &containerDloadInfo{
			cpuPath:    paths.Path(subsystem.SubsystemCPU, container.CgroupPath),
			cgroupName: container.CgroupPath,
			container:  container,
			isSeen:     true,
		}
	}
	for id, info := range d.containers {
		if !info.isSeen {
			delete(d.containers, id)
		}
	}
}

func (d *dloadTracing) shouldTrace(container *containerDloadInfo, sampledAt time.Time) bool {
	return d.threshold.shouldTrace(container, sampledAt)
}

func (threshold dloadThreshold) shouldTrace(container *containerDloadInfo, sampledAt time.Time) bool {
	if threshold.isDebug {
		return true
	}
	if container.dLoad[0] <= float64(threshold.load) {
		return false
	}
	if sampledAt.Sub(container.lastTraceAt) < threshold.minTraceInterval {
		return false
	}

	return true
}

func (d *dloadTracing) selectTraceTarget(
	sampledAt time.Time,
	mode cgroups.Mode,
) (*containerDloadInfo, cgroupStats.LoadStats, error) {
	var legacyReader *netlink.NetlinkReader
	var unifiedStats map[string]cgroupStats.LoadStats
	switch mode {
	case cgroups.Legacy, cgroups.Hybrid:
		var err error
		legacyReader, err = netlink.New()
		if err != nil {
			return nil, cgroupStats.LoadStats{}, fmt.Errorf(
				"open dload netlink connection: %w", err)
		}
		defer legacyReader.Stop()
	case cgroups.Unified:
		paths := make([]string, 0, len(d.containers))
		for _, container := range d.containers {
			paths = append(paths, container.cgroupName)
		}
		var err error
		unifiedStats, err = d.v2LoadStats(paths)
		if err != nil {
			if errors.Is(err, cgroupV2.ErrTaskIteratorNotSupported) {
				log.WithError(err).Warn(
					"cgroup v2 dload is unavailable and will remain stopped")
				return nil, cgroupStats.LoadStats{}, fmt.Errorf(
					"%w: cgroup v2 dload requires BPF task iterator support: %w",
					types.ErrNotSupported, err)
			}
			if len(unifiedStats) == 0 {
				return nil, cgroupStats.LoadStats{}, fmt.Errorf(
					"collect cgroup v2 dload snapshot: %w", err)
			}
			log.WithError(err).
				WithField("containers_collected", len(unifiedStats)).
				Debug("partially collected cgroup v2 dload snapshot")
		}
	case cgroups.Unavailable:
		return nil, cgroupStats.LoadStats{}, errors.New(
			"collect dload stats: cgroup filesystem is unavailable")
	default:
		return nil, cgroupStats.LoadStats{}, fmt.Errorf(
			"collect dload stats: unsupported cgroup mode %d", mode)
	}

	for _, container := range d.containers {
		stats, err := d.containerLoadStats(
			mode, legacyReader, unifiedStats, container)
		if err != nil {
			log.WithError(err).
				WithField("container_id", container.container.ID).
				WithField("hostname", container.container.Hostname).
				Debug("failed to read container cpu load")
			continue
		}

		updateLoad(container, stats.NrRunning, stats.NrUninterruptible, d.decayFactors)
		if !d.shouldTrace(container, sampledAt) {
			continue
		}

		log.WithField("container_id", container.container.ID).
			WithField("threshold", d.threshold.load).
			WithField("load_average", container.loadAvg[0]).
			WithField("dload_average", container.dLoad[0]).
			Info("dload threshold exceeded")
		return container, stats, nil
	}

	return nil, cgroupStats.LoadStats{}, nil
}

func (d *dloadTracing) containerLoadStats(
	mode cgroups.Mode,
	legacyReader *netlink.NetlinkReader,
	unifiedStats map[string]cgroupStats.LoadStats,
	container *containerDloadInfo,
) (cgroupStats.LoadStats, error) {
	if mode == cgroups.Unified {
		load, ok := unifiedStats[container.cgroupName]
		if !ok {
			return cgroupStats.LoadStats{}, fmt.Errorf(
				"cgroup v2 path %q disappeared", container.cgroupName)
		}
		return load, nil
	}

	load, err := legacyReader.GetCpuLoad(container.cgroupName, container.cpuPath)
	if err != nil {
		return cgroupStats.LoadStats{}, err
	}

	return legacyLoadStats(load), nil
}

func legacyLoadStats(load cadvisorV1.LoadStats) cgroupStats.LoadStats {
	return cgroupStats.LoadStats{
		NrSleeping:        load.NrSleeping,
		NrRunning:         load.NrRunning,
		NrStopped:         load.NrStopped,
		NrUninterruptible: load.NrUninterruptible,
		NrIoWait:          load.NrIoWait,
	}
}

func (d *dloadTracing) buildAndSave(
	container *containerDloadInfo,
	loadStats cgroupStats.LoadStats,
	stacks *dloadStackCapture,
) error {
	startedTimestamp := time.Now().UTC()
	cgroupPath := container.cgroupName
	containerID := ""
	scope := taskScopeHost
	threshold := d.hostThreshold
	if container.container != nil {
		containerID = container.container.ID
		scope = taskScopeCgroup
		threshold = d.threshold
	}

	cgroupStack, err := stacks.capture(
		scope,
		cgroupPath,
		threshold.isDebug,
	)
	if err != nil {
		return fmt.Errorf("capture dload task stacks: %w", err)
	}

	if cgroupStack == "" && !threshold.isDebug {
		return nil
	}

	hostStack := ""
	if scope == taskScopeCgroup {
		hostStack, err = stacks.capture(taskScopeHost, "", d.threshold.isDebug)
		if err != nil {
			return fmt.Errorf("capture host task stacks: %w", err)
		}
	}

	data := &DloadTracingData{
		NrSleeping:        loadStats.NrSleeping,
		NrRunning:         loadStats.NrRunning,
		NrStopped:         loadStats.NrStopped,
		NrUninterruptible: loadStats.NrUninterruptible,
		NrIoWait:          loadStats.NrIoWait,
		LoadAvg:           container.loadAvg[0],
		DLoadAvg:          container.dLoad[0],
		Threshold:         uint64(threshold.load),
		Stack:             cgroupStack + hostStack,
	}

	// Check if this is caused by known issues.
	cfg := configSnapshot()
	knownIssue, _ := matcher.Classify(cfg.IssuesList, cgroupStack)
	data.KnownIssue = knownIssue

	if err := tracing.Save(&tracing.WriteRequest{
		TracerName:       "dload",
		ContainerID:      containerID,
		StartedTimestamp: startedTimestamp,
		TracerData:       data,
		TracerRunType:    types.TracerRunTypeAutotracing,
	}); err != nil {
		return fmt.Errorf("save dload trace: %w", err)
	}
	return nil
}

const (
	maxDurationSeconds = int64(time.Duration(1<<63-1) / time.Second)
	loadFractionBits   = 11
	fixedOne           = 1 << loadFractionBits
	loadOneMinute      = time.Minute
	loadFiveMinutes    = 5 * time.Minute
)

func loadDecayFactor(interval, window time.Duration) uint64 {
	return uint64(math.Round(
		math.Exp(-float64(interval)/float64(window)) * fixedOne,
	))
}

func loadDecayFactors(interval time.Duration) [2]uint64 {
	return [2]uint64{
		loadDecayFactor(interval, loadOneMinute),
		loadDecayFactor(interval, loadFiveMinutes),
	}
}

func calcLoad(load, exp, active uint64) uint64 {
	newLoad := load*exp + active*(fixedOne-exp)
	newLoad += 1 << (loadFractionBits - 1)

	return newLoad / fixedOne
}

func calcLoadAvg(previous [2]uint64, active uint64, decayFactors [2]uint64) [2]uint64 {
	active *= fixedOne

	return [2]uint64{
		calcLoad(previous[0], decayFactors[0], active),
		calcLoad(previous[1], decayFactors[1], active),
	}
}

func loadInt(load uint64) uint64 {
	return load >> loadFractionBits
}

func loadFraction(load uint64) uint64 {
	return loadInt((load & (fixedOne - 1)) * 100)
}

func loadAverages(averages [2]uint64, offset uint64, shift int) [2]float64 {
	loads := [2]uint64{
		(averages[0] + offset) << shift,
		(averages[1] + offset) << shift,
	}

	return [2]float64{
		float64(loadInt(loads[0])) + float64(loadFraction(loads[0]))/100,
		float64(loadInt(loads[1])) + float64(loadFraction(loads[1]))/100,
	}
}

func updateLoad(
	info *containerDloadInfo,
	nrRunning, nrUninterruptible uint64,
	decayFactors [2]uint64,
) {
	info.runnableAvg = calcLoadAvg(
		info.runnableAvg, nrRunning+nrUninterruptible, decayFactors)
	info.loadAvg = loadAverages(info.runnableAvg, fixedOne/200, 0)
	info.dLoadAvg = calcLoadAvg(info.dLoadAvg, nrUninterruptible, decayFactors)
	info.dLoad = loadAverages(info.dLoadAvg, fixedOne/200, 0)
}

func pidStack(pid int32) string {
	data, _ := os.ReadFile(fmt.Sprintf("/proc/%d/stack", pid))
	return string(data)
}

func cgroupHostTasks(scope taskScope, path string) ([]int32, error) {
	switch scope {
	case taskScopeCgroup:
		cgroup, err := cgroups.NewManager()
		if err != nil {
			return nil, err
		}

		return cgroup.Pids(path)
	case taskScopeHost:
		procs, err := procfs.AllProcs()
		if err != nil {
			return nil, err
		}

		pidList := make([]int32, 0, len(procs))
		for _, p := range procs {
			threads, err := os.ReadDir(fmt.Sprintf("/proc/%d/task", p.PID))
			if err != nil {
				continue
			}
			for _, thread := range threads {
				var tid int32
				if _, err := fmt.Sscan(thread.Name(), &tid); err == nil {
					pidList = append(pidList, tid)
				}
			}
		}
		return pidList, nil
	default:
		return nil, fmt.Errorf("unsupported task scope %d", scope)
	}
}

// A capture belongs to one sampling tick; host and container events can share
// its host stacks, including an empty result or error, but not across ticks.
type dloadStackCapture struct {
	dump         func(taskScope, string, bool) (string, error)
	hostStack    string
	hostErr      error
	hostCaptured bool
}

func (s *dloadStackCapture) capture(scope taskScope, path string, all bool) (string, error) {
	if scope != taskScopeHost {
		return s.dump(scope, path, all)
	}
	if !s.hostCaptured {
		s.hostStack, s.hostErr = s.dump(scope, path, all)
		s.hostCaptured = true
	}
	return s.hostStack, s.hostErr
}

func dumpUninterruptibleTaskStack(scope taskScope, path string, all bool) (string, error) {
	tasks, err := cgroupHostTasks(scope, path)
	if err != nil {
		return "", err
	}

	var stacks bytes.Buffer
	for _, pid := range tasks {
		proc, err := process.NewProcess(pid)
		if err != nil {
			continue
		}

		status, err := proc.Status()
		if err != nil {
			continue
		}

		if status != "D" && status != "U" && !all {
			continue
		}
		comm, err := proc.Name()
		if err != nil {
			continue
		}
		stack := pidStack(pid)
		if stack == "" {
			continue
		}

		fmt.Fprintf(&stacks, "Comm: %s\tPid: %d\n%s\n", comm, pid, stack)
	}

	if stacks.Len() == 0 {
		return "", nil
	}

	title := "\nstacktrace of D task in cgroup:\n"
	if scope == taskScopeHost {
		title = "\nstacktrace of D task in host:\n"
	}
	return title + stacks.String(), nil
}

// Start detect work, monitor the load of containers.
func (d *dloadTracing) Start(ctx context.Context) error {
	mode := cgroups.CgroupMode()
	defer cgroupV2.ForgetSharedLoadStatsConsumer(cgroupV2.LoadStatsConsumerDload)

	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return types.ErrExitByCancelCtx
		case sampledAt := <-ticker.C:
			stacks := dloadStackCapture{dump: dumpUninterruptibleTaskStack}
			d.hostStats = nil
			if !d.hostUnsupported && mode != cgroups.Unified {
				_, host, err := cgroupV2.SharedLoadStatsWithHost(cgroupV2.LoadStatsConsumerDload, nil)
				d.hostStats = host
				if err != nil {
					log.WithError(err).Warn("host dload snapshot unavailable")
					if errors.Is(err, cgroupV2.ErrTaskIteratorNotSupported) {
						d.hostUnsupported = true
					}
				}
			}
			containers, err := pod.Containers()
			if err != nil {
				if !d.hostUnsupported && mode == cgroups.Unified {
					_, d.hostStats, _ = cgroupV2.SharedLoadStatsWithHost(cgroupV2.LoadStatsConsumerDload, nil)
				}
				d.traceHost(sampledAt, &stacks)
				if !d.hostUnsupported {
					log.WithError(err).Warn("list containers for dload sampling")
					continue
				}
				return fmt.Errorf("list containers for dload sampling: %w", err)
			}
			d.reconcileContainers(containers)

			container, loadStats, err := d.selectTraceTarget(sampledAt, mode)
			d.traceHost(sampledAt, &stacks)
			if err != nil {
				if !d.hostUnsupported && !errors.Is(err, types.ErrNotSupported) {
					log.WithError(err).Warn("container dload snapshot unavailable")
					continue
				}
				return err
			}
			if container == nil {
				continue
			}

			if err := d.buildAndSave(container, loadStats, &stacks); err != nil {
				return err
			}
			container.lastTraceAt = sampledAt
		}
	}
}

func (d *dloadTracing) traceHost(sampledAt time.Time, stacks *dloadStackCapture) {
	if d.hostStats == nil {
		return
	}
	updateLoad(&d.host, d.hostStats.NrRunning, d.hostStats.NrUninterruptible, d.decayFactors)
	if !d.hostThreshold.shouldTrace(&d.host, sampledAt) {
		return
	}
	// Keep capture attempts rate-limited even when saving fails.
	d.host.lastTraceAt = sampledAt
	if err := d.buildAndSave(&d.host, *d.hostStats, stacks); err != nil {
		log.WithError(err).Warn("save host dload trace")
	}
}
