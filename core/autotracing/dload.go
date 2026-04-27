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
	"bytes"
	"context"
	"fmt"
	"os"
	"time"

	"huatuo-bamai/internal/cgroups"
	"huatuo-bamai/internal/cgroups/paths"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pattern"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/storage"
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
	return &tracing.EventTracingAttr{
		TracingData: &dloadTracing{},
		Interval:    30,
		Flag:        tracing.FlagTracing,
	}, nil
}

type containerDloadInfo struct {
	path      string
	name      string
	container *pod.Container
	avgnrun   [2]uint64
	load      [2]float64
	avgnuni   [2]uint64
	loaduni   [2]float64
	alive     bool
	traceTime time.Time
}

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
	taskHostType   = 1
	taskCgroupType = 2
)

const debugDload = false

type containersDloadMap map[string]*containerDloadInfo

var containersDloads = make(containersDloadMap)

func updateContainersDload() error {
	containers, err := pod.Containers()
	if err != nil {
		return err
	}

	for _, container := range containers {
		if _, ok := containersDloads[container.ID]; ok {
			containersDloads[container.ID].name = container.CgroupPath
			containersDloads[container.ID].path = paths.Path("cpu", container.CgroupPath)
			containersDloads[container.ID].container = container
			containersDloads[container.ID].alive = true
			continue
		}

		containersDloads[container.ID] = &containerDloadInfo{
			path:      paths.Path("cpu", container.CgroupPath),
			name:      container.CgroupPath,
			container: container,
			alive:     true,
		}
	}

	return nil
}

func shouldCareThisLoadEvent(container *containerDloadInfo, threshold *dloadThreshold) bool {
	nowtime := time.Now()
	intervalTracing := nowtime.Sub(container.traceTime)

	if int64(intervalTracing.Seconds()) > threshold.intervalTracing {
		if container.loaduni[0] > float64(threshold.thresh) {
			container.traceTime = nowtime
			return true
		}
	}

	if debugDload {
		return true
	}

	return false
}

func detectDloadContainer(threshold *dloadThreshold) (*containerDloadInfo, cadvisorV1.LoadStats, error) {
	empty := cadvisorV1.LoadStats{}

	n, err := netlink.New()
	if err != nil {
		return nil, empty, err
	}
	defer n.Stop()

	for id, container := range containersDloads {
		if !container.alive {
			delete(containersDloads, id)
		} else {
			container.alive = false

			stats, err := n.GetCpuLoad(container.name, container.path)
			if err != nil {
				log.Debugf("failed to get %s load, probably the container has been deleted: %s", container.container.Hostname, err)
				continue
			}

			updateLoad(container, stats.NrRunning, stats.NrUninterruptible)

			if shouldCareThisLoadEvent(container, threshold) {
				log.Infof("dload event: Threshold=%0.2f %+v, LoadAvg=%0.2f, DLoadAvg=%0.2f",
					float64(threshold.thresh), stats, container.load[0], container.loaduni[0])
				return container, stats, nil
			}
		}
	}

	return nil, empty, fmt.Errorf("no dload containers")
}

func buildAndSaveDloadContainer(thresh *dloadThreshold, container *containerDloadInfo, loadstat cadvisorV1.LoadStats) error {
	cgrpPath := container.name
	containerID := container.container.ID

	stackCgrp, err := dumpUninterruptibleTaskStack(taskCgroupType, cgrpPath, debugDload)
	if err != nil {
		return err
	}

	if stackCgrp == "" {
		return nil
	}

	stackHost, err := dumpUninterruptibleTaskStack(taskHostType, "", debugDload)
	if err != nil {
		return err
	}

	data := &DloadTracingData{
		NrSleeping:        loadstat.NrSleeping,
		NrRunning:         loadstat.NrRunning,
		NrStopped:         loadstat.NrStopped,
		NrUninterruptible: loadstat.NrUninterruptible,
		NrIoWait:          loadstat.NrIoWait,
		LoadAvg:           container.load[0],
		DLoadAvg:          container.loaduni[0],
		Threshold:         uint64(thresh.thresh),
		Stack:             fmt.Sprintf("%s%s", stackCgrp, stackHost),
	}

	// Check if this is caused by known issues.
	knownIssue, _ := pattern.Match(cfg.IssuesList, stackCgrp)
	data.KnownIssue = knownIssue

	storage.Save("dload", containerID, time.Now(), data)
	return nil
}

const (
	fShift = 11
	fixed1 = 1 << fShift
	exp1   = 1884
	exp5   = 2014
	exp15  = 2037
)

func calcLoad(load, exp, active uint64) uint64 {
	var newload uint64

	newload = load*exp + active*(fixed1-exp)
	newload += 1 << (fShift - 1)

	return newload / fixed1
}

func calcLoadavg(avgnrun [2]uint64, active uint64) (avgnresult [2]uint64) {
	if active > 0 {
		active *= fixed1
	} else {
		active = 0
	}

	avgnresult[0] = calcLoad(avgnrun[0], exp1, active)
	avgnresult[1] = calcLoad(avgnrun[1], exp5, active)

	return avgnresult
}

func loadInt(x uint64) (r uint64) {
	r = x >> fShift
	return r
}

func loadFrac(x uint64) (r uint64) {
	r = loadInt((x & (fixed1 - 1)) * 100)
	return r
}

func getAvenrun(avgnrun [2]uint64, offset uint64, shift int) (loadavgNew [2]float64) {
	var loads [2]uint64

	loads[0] = (avgnrun[0] + offset) << shift
	loads[1] = (avgnrun[1] + offset) << shift

	loadavgNew[0] = float64(loadInt(loads[0])) +
		float64(loadFrac(loads[0]))/float64(100)

	loadavgNew[1] = float64(loadInt(loads[1])) +
		float64(loadFrac(loads[1]))/float64(100)

	return loadavgNew
}

func updateLoad(info *containerDloadInfo, nrRunning, nrUninterruptible uint64) {
	info.avgnrun = calcLoadavg(info.avgnrun, nrRunning+nrUninterruptible)
	info.load = getAvenrun(info.avgnrun, fixed1/200, 0)
	info.avgnuni = calcLoadavg(info.avgnuni, nrUninterruptible)
	info.loaduni = getAvenrun(info.avgnuni, fixed1/200, 0)
}

func pidStack(pid int32) string {
	data, _ := os.ReadFile(fmt.Sprintf("/proc/%d/stack", pid))
	return string(data)
}

func cgroupHostTasks(where int, path string) ([]int32, error) {
	switch where {
	case taskCgroupType:
		cgroup, err := cgroups.NewManager()
		if err != nil {
			return nil, err
		}

		return cgroup.Pids(path)
	case taskHostType:
		var pidList []int32

		procs, err := procfs.AllProcs()
		if err != nil {
			return nil, err
		}

		for _, p := range procs {
			pidList = append(pidList, int32(p.PID))
		}
		return pidList, err
	default:
		return nil, fmt.Errorf("type not supported")
	}
}

func dumpUninterruptibleTaskStack(where int, path string, all bool) (string, error) {
	var appended bool = false

	stacks := new(bytes.Buffer)

	tasks, err := cgroupHostTasks(where, path)
	if err != nil {
		return "", err
	}

	for _, pid := range tasks {
		proc, err := process.NewProcess(pid)
		if err != nil {
			continue
		}

		status, err := proc.Status()
		if err != nil {
			continue
		}

		if status == "D" || status == "U" || all {
			comm, err := proc.Name()
			if err != nil {
				continue
			}
			stack := pidStack(pid)
			if stack == "" {
				continue
			}

			fmt.Fprintf(stacks, "Comm: %s\tPid: %d\n%s\n", comm, pid, stack)
			appended = true
		}
	}

	if appended {
		title := "\nstacktrace of D task in cgroup:\n"
		if where == taskHostType {
			title = "\nstacktrace of D task in host:\n"
		}

		return fmt.Sprintf("%s%s", title, stacks), nil
	}

	return "", nil
}

type dloadTracing struct{}

type dloadThreshold struct {
	thresh          int64
	intervalTracing int64
}

// Start detect work, monitor the load of containers
func (c *dloadTracing) Start(ctx context.Context) error {
	interval := cfg.Dload.Interval

	thresh := &dloadThreshold{
		thresh:          cfg.Dload.ThresholdLoad,
		intervalTracing: cfg.Dload.IntervalTracing,
	}

	for {
		select {
		case <-ctx.Done():
			return types.ErrExitByCancelCtx
		default:
			time.Sleep(time.Duration(interval) * time.Second)

			if err := updateContainersDload(); err != nil {
				return err
			}

			container, loadstat, err := detectDloadContainer(thresh)
			if err != nil {
				continue
			}

			_ = buildAndSaveDloadContainer(thresh, container, loadstat)
		}
	}
}
