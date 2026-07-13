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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"

	internalconfig "huatuo-bamai/internal/config"
	"huatuo-bamai/internal/flamegraph"
	"huatuo-bamai/internal/log"
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

// minCPUFields is the minimum number of numeric fields the aggregate cpu line
// in /proc/stat must contain: user, nice, system. Anything fewer indicates a
// malformed or truncated file and the values cannot be trusted.
const minCPUFields = 3

// parseCPUStatLine parses the aggregate "cpu  ..." line from /proc/stat and
// returns the system (field index 2) and total (sum of all fields) usage.
// It is extracted from cpuSysUsage so it can be unit-tested without a real
// /proc/stat.
func parseCPUStatLine(line string) (system, total uint64, err error) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return 0, 0, fmt.Errorf("cpu: empty /proc/stat aggregate line")
	}
	// fields[0] is the "cpu" label; the rest are numeric counters.
	numbers := fields[1:]
	if len(numbers) < minCPUFields {
		return 0, 0, fmt.Errorf("cpu: /proc/stat aggregate line has %d fields, need at least %d", len(numbers), minCPUFields)
	}

	for i, field := range numbers {
		val, parseErr := strconv.ParseUint(field, 10, 64)
		if parseErr != nil {
			return 0, 0, fmt.Errorf("cpu: /proc/stat field %d (%q): %w", i, field, parseErr)
		}
		total += val
		if i == 2 {
			system = val
		}
	}

	return system, total, nil
}

func cpuSysUsage() (*cpuUsage, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		if scanErr := scanner.Err(); scanErr != nil {
			return nil, fmt.Errorf("cpu: read /proc/stat: %w", scanErr)
		}
		return nil, errors.New("cpu: /proc/stat is empty")
	}

	system, total, err := parseCPUStatLine(scanner.Text())
	if err != nil {
		return nil, err
	}

	return &cpuUsage{system: system, total: total}, nil
}

// safeSubUint64 returns a-b clamped to 0. /proc/stat counters are monotonic,
// but across container restarts, host migrations, or counter wraps the new
// sample can be smaller than the cached one, which would underflow uint64
// subtraction and produce an enormous (close to 2^64) delta.
func safeSubUint64(a, b uint64) uint64 {
	if a <= b {
		return 0
	}
	return a - b
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

	sysUsageDelta := safeSubUint64(usage.system, c.usage.system)
	sysTotalDelta := safeSubUint64(usage.total, c.usage.total)
	if sysTotalDelta == 0 {
		// No tick elapsed between samples (very fast polling on an idle CPU)
		// or counters went backwards so the delta was clamped to 0. Either
		// way we cannot compute a percentage; keep the previous baseline so
		// the next sample has a valid reference point instead of zeroing it.
		c.usage = usage
		return nil
	}

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
		"--bpf-path", path.Join(internalconfig.CoreBpfDir, "perf.o"),
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
	if err := tracing.Save(&tracing.WriteRequest{
		TracerName:    "cpusys",
		TracerTime:    traceTime,
		TracerData:    &tracerData,
		TracerRunType: tracing.TracerRunTypeAutotracing,
	}); err != nil {
		log.Warnf("failed to save tracing data: %v", err)
	}
	return nil
}

func (c *cpuSysTracing) Start(ctx context.Context) error {
	interval := cfg.CPUSys.Interval
	perfRunTimeOut := cfg.CPUSys.RunTracingToolTimeout

	threshold := &cpuSysThreshold{
		delta: cfg.CPUSys.DeltaSysThreshold,
		usage: cfg.CPUSys.SysThreshold,
	}

	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return types.ErrExitByCancelCtx
		case <-ticker.C:
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
