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
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"

	"huatuo-bamai/internal/conf"
	"huatuo-bamai/internal/flamegraph"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/storage"
	"huatuo-bamai/pkg/tracing"
	"huatuo-bamai/pkg/types"
)

func init() {
	tracing.RegisterEventTracing("cpusys", newCpuSys)
}

func newCpuSys() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &cpuSysTracing{},
		Interval:    20,
		Flag:        tracing.FlagTracing,
	}, nil
}

type cpuUsage struct {
	system uint64
	total  uint64
}

type cpuSysTracing struct {
	usage           *cpuUsage
	sysPercent      int64
	sysPercentDelta int64
}

type CpuSysTracingData struct {
	NowSys            int64                  `json:"now_sys"`
	SysThreshold      int64                  `json:"sys_threshold"`
	DeltaSys          int64                  `json:"deltasys"`
	DeltaSysThreshold int64                  `json:"deltasys_threshold"`
	FlameData         []flamegraph.FrameData `json:"flamedata"`
}

type cpuSysThreshold struct {
	delta int64
	usage int64
}

func cpuSysUsage() (*cpuUsage, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Scan()
	fields := strings.Fields(scanner.Text())[1:]

	var total, sys uint64
	for i, field := range fields {
		val, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return nil, err
		}

		total += val
		if i == 2 {
			sys = val
		}
	}

	return &cpuUsage{system: sys, total: total}, nil
}

func (c *cpuSysTracing) updateCpuSysUsage() error {
	usage, err := cpuSysUsage()
	if err != nil {
		return err
	}

	if c.usage == nil {
		c.usage = usage
		return nil
	}

	sysUsageDelta := usage.system - c.usage.system
	sysTotalDelta := usage.total - c.usage.total
	sysPercentage := int64(100 * sysUsageDelta / sysTotalDelta)

	c.sysPercentDelta = sysPercentage - c.sysPercent
	c.sysPercent = sysPercentage
	c.usage = usage
	return nil
}

func (c *cpuSysTracing) shouldCareThisEvent(threshold *cpuSysThreshold) bool {
	log.Debugf("sys %d, sys delta: %d", c.sysPercent, c.sysPercentDelta)

	if c.sysPercent > threshold.usage || c.sysPercentDelta > threshold.delta {
		return true
	}

	return false
}

func runPerfSystemWide(parent context.Context, timeOut int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, time.Duration(timeOut+30)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, path.Join(tracing.TaskBinDir, "perf"),
		"--bpf-obj", "cpuidle.o",
		"--duration", strconv.FormatInt(timeOut, 10))

	return cmd.CombinedOutput()
}

func (c *cpuSysTracing) buildAndSaveCPUSystem(traceTime time.Time, threshold *cpuSysThreshold, flamedata []byte) error {
	tracerData := CpuSysTracingData{
		NowSys:            c.sysPercent,
		SysThreshold:      threshold.usage,
		DeltaSys:          c.sysPercentDelta,
		DeltaSysThreshold: threshold.delta,
	}

	if err := json.Unmarshal(flamedata, &tracerData.FlameData); err != nil {
		return err
	}

	log.Debugf("cpuidle flamedata %v", tracerData.FlameData)
	storage.Save("cpusys", "", traceTime, &tracerData)
	return nil
}

func (c *cpuSysTracing) Start(ctx context.Context) error {
	interval := conf.Get().AutoTracing.CPUSys.Interval
	perfRunTimeOut := conf.Get().AutoTracing.CPUSys.PerfRunTimeOut

	threshold := &cpuSysThreshold{
		delta: conf.Get().AutoTracing.CPUSys.DeltaSysThreshold,
		usage: conf.Get().AutoTracing.CPUSys.SysThreshold,
	}

	for {
		select {
		case <-ctx.Done():
			return types.ErrExitByCancelCtx
		case <-time.After(time.Duration(interval) * time.Second):
			if err := c.updateCpuSysUsage(); err != nil {
				return err
			}

			if ok := c.shouldCareThisEvent(threshold); !ok {
				continue
			}

			traceTime := time.Now()

			log.Infof("start perf system wide, cpu sys: %d, delta: %d, perf_run_timeout: %d",
				c.sysPercent, c.sysPercentDelta, perfRunTimeOut)
			flamedata, err := runPerfSystemWide(ctx, perfRunTimeOut)
			if err != nil {
				log.Debugf("perf err: %v, output: %v", err, string(flamedata))
				return err
			}

			if len(flamedata) == 0 {
				log.Infof("perf output is null for system usage")
				continue
			}

			if err := c.buildAndSaveCPUSystem(traceTime, threshold, flamedata); err != nil {
				return err
			}
		}
	}
}
