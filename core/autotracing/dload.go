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
	"os"
	"time"

	"huatuo-bamai/internal/cgroups"
	"huatuo-bamai/internal/cgroups/paths"
	"huatuo-bamai/internal/cgroups/subsystem"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/matcher"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/pkg/tracing"
	"huatuo-bamai/pkg/types"

	cadvisorV1 "github.com/google/cadvisor/info/v1"
	"github.com/google/cadvisor/utils/cpuload/netlink"
	"github.com/prometheus/procfs"
	"github.com/shirou/gopsutil/process"
)

func init() {
	tracing.RegisterEventTracing("dload", newDload)
}

func newDload() (*tracing.EventTracingAttr, error) {
	tracer, err := newDloadTracing(cfg)
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
	containers map[string]*containerDloadInfo
	interval   time.Duration
	threshold  dloadThreshold
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
	if config.Dload.IntervalTracing < 0 {
		return nil, errors.New("dload tracing interval must be non-negative")
	}
	if config.Dload.ThresholdLoad < 0 {
		return nil, errors.New("dload threshold must be non-negative")
	}

	return &dloadTracing{
		containers: make(map[string]*containerDloadInfo),
		interval:   time.Duration(config.Dload.Interval) * time.Second,
		threshold: dloadThreshold{
			load:             config.Dload.ThresholdLoad,
			minTraceInterval: time.Duration(config.Dload.IntervalTracing) * time.Second,
			isDebug:          config.Dload.EnableDebug,
		},
	}, nil
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
	if d.threshold.isDebug {
		return true
	}
	if container.dLoad[0] <= float64(d.threshold.load) {
		return false
	}
	if sampledAt.Sub(container.lastTraceAt) < d.threshold.minTraceInterval {
		return false
	}

	return true
}

func (d *dloadTracing) selectTraceTarget(
	sampledAt time.Time,
) (*containerDloadInfo, cadvisorV1.LoadStats, error) {
	n, err := netlink.New()
	if err != nil {
		return nil, cadvisorV1.LoadStats{}, fmt.Errorf("open dload netlink connection: %w", err)
	}
	defer n.Stop()

	for _, container := range d.containers {
		stats, err := n.GetCpuLoad(container.cgroupName, container.cpuPath)
		if err != nil {
			log.WithError(err).
				WithField("container_id", container.container.ID).
				WithField("hostname", container.container.Hostname).
				Debug("failed to read container cpu load")
			continue
		}

		updateLoad(container, stats.NrRunning, stats.NrUninterruptible)
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

	return nil, cadvisorV1.LoadStats{}, nil
}

func (d *dloadTracing) buildAndSave(
	container *containerDloadInfo,
	loadStats cadvisorV1.LoadStats,
) error {
	cgroupPath := container.cgroupName
	containerID := container.container.ID

	cgroupStack, err := dumpUninterruptibleTaskStack(
		taskScopeCgroup,
		cgroupPath,
		d.threshold.isDebug,
	)
	if err != nil {
		return fmt.Errorf("capture container task stacks: %w", err)
	}

	if cgroupStack == "" && !d.threshold.isDebug {
		return nil
	}

	hostStack, err := dumpUninterruptibleTaskStack(taskScopeHost, "", d.threshold.isDebug)
	if err != nil {
		return fmt.Errorf("capture host task stacks: %w", err)
	}

	data := &DloadTracingData{
		NrSleeping:        loadStats.NrSleeping,
		NrRunning:         loadStats.NrRunning,
		NrStopped:         loadStats.NrStopped,
		NrUninterruptible: loadStats.NrUninterruptible,
		NrIoWait:          loadStats.NrIoWait,
		LoadAvg:           container.loadAvg[0],
		DLoadAvg:          container.dLoad[0],
		Threshold:         uint64(d.threshold.load),
		Stack:             cgroupStack + hostStack,
	}

	// Check if this is caused by known issues.
	knownIssue, _ := matcher.Classify(cfg.IssuesList, cgroupStack)
	data.KnownIssue = knownIssue

	if err := tracing.Save(&tracing.WriteRequest{
		TracerName:    "dload",
		ContainerID:   containerID,
		TracerTime:    time.Now(),
		TracerData:    data,
		TracerRunType: tracing.TracerRunTypeAutotracing,
	}); err != nil {
		return fmt.Errorf("save dload trace: %w", err)
	}
	return nil
}

const (
	loadFractionBits = 11
	fixedOne         = 1 << loadFractionBits
	expOneMinute     = 1884
	expFiveMinutes   = 2014
)

func calcLoad(load, exp, active uint64) uint64 {
	newLoad := load*exp + active*(fixedOne-exp)
	newLoad += 1 << (loadFractionBits - 1)

	return newLoad / fixedOne
}

func calcLoadAvg(previous [2]uint64, active uint64) [2]uint64 {
	active *= fixedOne

	return [2]uint64{
		calcLoad(previous[0], expOneMinute, active),
		calcLoad(previous[1], expFiveMinutes, active),
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

func updateLoad(info *containerDloadInfo, nrRunning, nrUninterruptible uint64) {
	info.runnableAvg = calcLoadAvg(info.runnableAvg, nrRunning+nrUninterruptible)
	info.loadAvg = loadAverages(info.runnableAvg, fixedOne/200, 0)
	info.dLoadAvg = calcLoadAvg(info.dLoadAvg, nrUninterruptible)
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
			pidList = append(pidList, int32(p.PID))
		}
		return pidList, nil
	default:
		return nil, fmt.Errorf("unsupported task scope %d", scope)
	}
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
// CGROUPSTATS_CMD_GET netlink API only works with cgroup v1.
func (d *dloadTracing) Start(ctx context.Context) error {
	if cgroups.CgroupMode() != cgroups.Legacy {
		log.Info("skipping dload tracing because cgroup v2 lacks load statistics")
		<-ctx.Done()
		return types.ErrExitByCancelCtx
	}

	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return types.ErrExitByCancelCtx
		case sampledAt := <-ticker.C:
			containers, err := pod.Containers()
			if err != nil {
				return fmt.Errorf("list containers for dload sampling: %w", err)
			}
			d.reconcileContainers(containers)

			container, loadStats, err := d.selectTraceTarget(sampledAt)
			if err != nil {
				return err
			}
			if container == nil {
				continue
			}

			if err := d.buildAndSave(container, loadStats); err != nil {
				return err
			}
			container.lastTraceAt = sampledAt
		}
	}
}
