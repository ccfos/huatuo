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
	"math"
	"os/exec"
	"path"
	"strconv"
	"time"

	"huatuo-bamai/internal/cgroups"
	"huatuo-bamai/internal/cgroups/stats"
	"huatuo-bamai/internal/conf"
	"huatuo-bamai/internal/flamegraph"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/storage"
	"huatuo-bamai/pkg/tracing"
	"huatuo-bamai/pkg/types"
)

func init() {
	tracing.RegisterEventTracing("cpuidle", newCPUIdle)
}

var cgroupMgr cgroups.Cgroup

func newCPUIdle() (*tracing.EventTracingAttr, error) {
	cgroupMgr, _ = cgroups.NewCgroupManager()

	return &tracing.EventTracingAttr{
		TracingData: &cpuIdleTracing{},
		Internal:    20,
		Flag:        tracing.FlagTracing,
	}, nil
}

type cpuIdleTracing struct{}

type cpuStats struct {
	user  int64
	sys   int64
	total int64
}

type containerCPUInfo struct {
	prevUsagePercentage  cpuStats
	nowUsagePercentage   cpuStats
	deltaUsagePercentage cpuStats
	prevUsage            cpuStats
	path                 string
	alive                bool
	id                   string
	traceTime            time.Time
	updateTime           time.Time
}

type cpuIdleThreshold struct {
	deltaUser              int64
	deltaSys               int64
	deltaTotal             int64
	usageUser              int64
	usageSys               int64
	usageTotal             int64
	intervalContinuousPerf int64
}

// containersCPUIdleMap is the container information
type containersCPUIdleMap map[string]*containerCPUInfo

var containersCPUIdle = make(containersCPUIdleMap)

func updateContainersCPUIdle() error {
	containers, err := pod.GetNormalContainers()
	if err != nil {
		return err
	}

	for _, container := range containers {
		if _, ok := containersCPUIdle[container.ID]; ok {
			containersCPUIdle[container.ID].path = container.CgroupSuffix
			containersCPUIdle[container.ID].alive = true
			containersCPUIdle[container.ID].id = container.ID
			continue
		}

		containersCPUIdle[container.ID] = &containerCPUInfo{
			path:  container.CgroupSuffix,
			alive: true,
			id:    container.ID,
		}
	}

	return nil
}

func detectCPUIdleContainer(threshold *cpuIdleThreshold) (*containerCPUInfo, error) {
	for id, container := range containersCPUIdle {
		if !container.alive {
			delete(containersCPUIdle, id)
		} else {
			container.alive = false

			if err := updateContainerCpuUsage(container); err != nil {
				log.Debugf("cpuidle update container [%s]: %v", container.path, err)
				continue
			}

			log.Debugf("container [%s], usage: %v", container.path, container.nowUsagePercentage)

			if shouldCareThisEvent(container, threshold) {
				return container, nil
			}
		}
	}

	return nil, fmt.Errorf("no cpuidle containers")
}

func containerCpuUsage(usage *stats.CpuUsage) cpuStats {
	return cpuStats{
		user:  int64(usage.User),
		sys:   int64(usage.System),
		total: int64(usage.Usage),
	}
}

func containerCpuUsageDelta(cpu1, cpu2 *cpuStats) cpuStats {
	return cpuStats{
		user:  cpu1.user - cpu2.user,
		sys:   cpu1.sys - cpu2.sys,
		total: cpu1.total - cpu2.total,
	}
}

func updateContainerCpuUsage(container *containerCPUInfo) error {
	cpuQuotaPeriod, err := cgroupMgr.CpuQuotaAndPeriod(container.path)
	if err != nil {
		return err
	}

	if cpuQuotaPeriod.Quota == math.MaxUint64 {
		return fmt.Errorf("cpu too large")
	}

	cpuCores := int64(cpuQuotaPeriod.Quota / cpuQuotaPeriod.Period)
	if cpuCores == 0 {
		return fmt.Errorf("cpu too small")
	}

	usage, err := cgroupMgr.CpuUsage(container.path)
	if err != nil {
		return err
	}

	if container.prevUsage == (cpuStats{}) {
		container.prevUsage = containerCpuUsage(usage)
		container.updateTime = time.Now()
		return fmt.Errorf("cpu usage first update")
	}

	delta := containerCpuUsageDelta(
		&cpuStats{
			user:  int64(usage.User),
			sys:   int64(usage.System),
			total: int64(usage.Usage),
		}, &container.prevUsage)
	if delta.total == 0 {
		container.updateTime = time.Now()
		return fmt.Errorf("cpu usage no changed")
	}

	updateElasped := time.Since(container.updateTime).Microseconds()

	container.nowUsagePercentage.user = 100 * delta.user / updateElasped / cpuCores
	container.nowUsagePercentage.sys = 100 * delta.sys / updateElasped / cpuCores
	container.nowUsagePercentage.total = 100 * delta.total / updateElasped / cpuCores

	if container.prevUsagePercentage == (cpuStats{}) {
		container.prevUsagePercentage = container.nowUsagePercentage
	}

	container.deltaUsagePercentage = containerCpuUsageDelta(
		&container.nowUsagePercentage,
		&container.prevUsagePercentage)
	container.prevUsagePercentage = container.nowUsagePercentage
	container.prevUsage = containerCpuUsage(usage)
	container.updateTime = time.Now()
	return nil
}

func shouldCareThisEvent(container *containerCPUInfo, threshold *cpuIdleThreshold) bool {
	nowtime := time.Now()
	intervalContinuousPerf := nowtime.Sub(container.traceTime)

	if int64(intervalContinuousPerf.Seconds()) > threshold.intervalContinuousPerf {
		if (container.nowUsagePercentage.user > threshold.usageUser &&
			container.deltaUsagePercentage.user > threshold.deltaUser) ||
			(container.nowUsagePercentage.sys > threshold.usageSys &&
				container.deltaUsagePercentage.sys > threshold.deltaSys) ||
			(container.nowUsagePercentage.total > threshold.usageTotal &&
				container.deltaUsagePercentage.total > threshold.deltaTotal) {
			container.traceTime = nowtime
			container.prevUsage = cpuStats{}
			return true
		}
	}

	return false
}

func runPerf(parent context.Context, containerId string, timeOut int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, time.Duration(timeOut+30)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, path.Join(conf.CoreBinDir, "perf"),
		"--bpf-obj", "cpuidle.o",
		"--container-id", containerId,
		"--duration", strconv.FormatInt(timeOut, 10))

	return cmd.CombinedOutput()
}

func buildAndSaveCPUIdleContainer(container *containerCPUInfo, threshold *cpuIdleThreshold, flamedata []byte) error {
	tracerData := CPUIdleTracingData{
		NowUser:             container.nowUsagePercentage.user,
		DeltaUser:           container.deltaUsagePercentage.user,
		UserThreshold:       threshold.usageUser,
		DeltaUserThreshold:  threshold.deltaUser,
		NowSys:              container.nowUsagePercentage.sys,
		DeltaSys:            container.deltaUsagePercentage.sys,
		SysThreshold:        threshold.usageSys,
		DeltaSysThreshold:   threshold.deltaSys,
		NowUsage:            container.nowUsagePercentage.total,
		DeltaUsage:          container.deltaUsagePercentage.total,
		UsageThreshold:      threshold.usageTotal,
		DeltaUsageThreshold: threshold.deltaTotal,
	}

	if err := json.Unmarshal(flamedata, &tracerData.FlameData); err != nil {
		return err
	}

	log.Debugf("cpuidle flamedata %v", tracerData.FlameData)
	storage.Save("cpuidle", container.id, container.traceTime, &tracerData)
	return nil
}

type CPUIdleTracingData struct {
	NowUser             int64                  `json:"user"`
	UserThreshold       int64                  `json:"user_threshold"`
	DeltaUser           int64                  `json:"deltauser"`
	DeltaUserThreshold  int64                  `json:"deltauser_threshold"`
	NowSys              int64                  `json:"sys"`
	SysThreshold        int64                  `json:"sys_threshold"`
	DeltaSys            int64                  `json:"deltasys"`
	DeltaSysThreshold   int64                  `json:"deltasys_threshold"`
	NowUsage            int64                  `json:"usage"`
	UsageThreshold      int64                  `json:"usage_threshold"`
	DeltaUsage          int64                  `json:"deltausage"`
	DeltaUsageThreshold int64                  `json:"deltausage_threshold"`
	FlameData           []flamegraph.FrameData `json:"flamedata"`
}

func (c *cpuIdleTracing) Start(ctx context.Context) error {
	interval := conf.Get().Tracing.CPUIdle.Interval
	perfRunTimeOut := conf.Get().Tracing.CPUIdle.PerfRunTimeOut

	threshold := &cpuIdleThreshold{
		deltaUser:              conf.Get().Tracing.CPUIdle.DeltaUserThreshold,
		deltaSys:               conf.Get().Tracing.CPUIdle.DeltaSysThreshold,
		deltaTotal:             conf.Get().Tracing.CPUIdle.DeltaUsageThreshold,
		usageUser:              conf.Get().Tracing.CPUIdle.UserThreshold,
		usageSys:               conf.Get().Tracing.CPUIdle.SysThreshold,
		usageTotal:             conf.Get().Tracing.CPUIdle.UsageThreshold,
		intervalContinuousPerf: conf.Get().Tracing.CPUIdle.IntervalContinuousPerf,
	}

	for {
		select {
		case <-ctx.Done():
			return types.ErrExitByCancelCtx
		case <-time.After(time.Duration(interval) * time.Second):
			if err := updateContainersCPUIdle(); err != nil {
				return err
			}

			container, err := detectCPUIdleContainer(threshold)
			if err != nil {
				continue
			}

			log.Infof("start perf container [%s], id [%s] with usage: %v, perf_run_timeout: %d",
				container.path, container.id,
				container.nowUsagePercentage,
				perfRunTimeOut)
			flamedata, err := runPerf(ctx, container.id, perfRunTimeOut)
			if err != nil {
				log.Debugf("perf err: %v, output: %v", err, string(flamedata))
				return err
			}

			if len(flamedata) == 0 {
				log.Infof("perf output is null for container id [%s]", container.id)
				continue
			}

			_ = buildAndSaveCPUIdleContainer(container, threshold, flamedata)
		}
	}
}
