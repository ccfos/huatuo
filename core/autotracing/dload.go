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
	"huatuo-bamai/internal/conf"
	"huatuo-bamai/internal/log"
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
		Internal:    30,
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
}

type DloadTracingData struct {
	Threshold         float64 `json:"threshold"`
	NrSleeping        uint64  `json:"nr_sleeping"`
	NrRunning         uint64  `json:"nr_running"`
	NrStopped         uint64  `json:"nr_stopped"`
	NrUninterruptible uint64  `json:"nr_uninterruptible"`
	NrIoWait          uint64  `json:"nr_iowait"`
	LoadAvg           float64 `json:"load_avg"`
	DLoadAvg          float64 `json:"dload_avg"`
	KnowIssue         string  `json:"known_issue"`
	InKnownList       uint64  `json:"in_known_list"`
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
			containersDloads[container.ID].name = container.CgroupSuffix
			containersDloads[container.ID].path = paths.Path("cpu", container.CgroupSuffix)
			containersDloads[container.ID].container = container
			containersDloads[container.ID].alive = true
			continue
		}

		containersDloads[container.ID] = &containerDloadInfo{
			path:      paths.Path("cpu", container.CgroupSuffix),
			name:      container.CgroupSuffix,
			container: container,
			alive:     true,
		}
	}

	return nil
}

func detectDloadContainer(thresh float64, interval int) (*containerDloadInfo, cadvisorV1.LoadStats, error) {
	empty := cadvisorV1.LoadStats{}

	n, err := netlink.New()
	if err != nil {
		return nil, empty, err
	}
	defer n.Stop()

	for containerId, dload := range containersDloads {
		if !dload.alive {
			delete(containersDloads, containerId)
		} else {
			dload.alive = false

			timeStart := dload.container.StartedAt.Add(time.Second * time.Duration(interval))
			if time.Now().Before(timeStart) {
				log.Debugf("%s were just started, we'll start monitoring it later.", dload.container.Hostname)
				continue
			}

			stats, err := n.GetCpuLoad(dload.name, dload.path)
			if err != nil {
				log.Debugf("failed to get %s load, probably the container has been deleted: %s", dload.container.Hostname, err)
				continue
			}

			updateLoad(dload, stats.NrRunning, stats.NrUninterruptible)

			if dload.loaduni[0] > thresh || debugDload {
				log.Infof("dload event: Threshold=%0.2f %+v, LoadAvg=%0.2f, DLoadAvg=%0.2f",
					thresh, stats, dload.load[0], dload.loaduni[0])

				return dload, stats, nil
			}
		}
	}

	return nil, empty, fmt.Errorf("no dload containers")
}

func buildAndSaveDloadContainer(thresh float64, container *containerDloadInfo, loadstat cadvisorV1.LoadStats) error {
	cgrpPath := container.name
	containerID := container.container.ID
	containerHostNamespace := container.container.LabelHostNamespace()

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
		Threshold:         thresh,
		Stack:             fmt.Sprintf("%s%s", stackCgrp, stackHost),
	}

	// Check if this is caused by known issues.
	knownIssue, inKnownList := conf.KnownIssueSearch(stackCgrp, containerHostNamespace, "")
	if knownIssue != "" {
		data.KnowIssue = knownIssue
		data.InKnownList = inKnownList
	} else {
		data.KnowIssue = "none"
		data.InKnownList = inKnownList
	}

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
		cgroup, err := cgroups.NewCgroupManager()
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

// Start detect work, monitor the load of containers
func (c *dloadTracing) Start(ctx context.Context) error {
	thresh := conf.Get().AutoTracing.Dload.ThresholdLoad
	interval := conf.Get().AutoTracing.Dload.MonitorGap

	for {
		select {
		case <-ctx.Done():
			return types.ErrExitByCancelCtx
		default:
			time.Sleep(5 * time.Second)

			if err := updateContainersDload(); err != nil {
				return err
			}

			container, loadstat, err := detectDloadContainer(thresh, interval)
			if err != nil {
				continue
			}

			_ = buildAndSaveDloadContainer(thresh, container, loadstat)
		}
	}
}
