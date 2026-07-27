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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"huatuo-bamai/internal/flamegraph"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/pkg/tracing"
	"huatuo-bamai/pkg/types"
)

const (
	cpuSysTracerName = "cpusys"
)

func init() {
	tracing.RegisterEventTracing(cpuSysTracerName, newCPUSys)
}

func newCPUSys() (*tracing.EventTracingAttr, error) {
	intervalSeconds := cfg.CPUSys.Interval
	minTraceIntervalSeconds := cfg.CPUSys.IntervalTracing
	perfDurationSeconds := cfg.CPUSys.RunTracingToolTimeout
	threshold := cpuSysThreshold{
		delta: cfg.CPUSys.DeltaSysThreshold,
		usage: cfg.CPUSys.SysThreshold,
	}
	if err := validateCPUConfig(cpuTracingConfig{
		intervalSeconds:         intervalSeconds,
		minTraceIntervalSeconds: minTraceIntervalSeconds,
		perfDurationSeconds:     perfDurationSeconds,
		systemThreshold:         threshold.usage,
		systemDeltaThreshold:    threshold.delta,
	}); err != nil {
		return nil, fmt.Errorf("validate cpu system config: %w", err)
	}

	return &tracing.EventTracingAttr{
		TracingData: &cpuSysTracing{
			interval:         time.Duration(intervalSeconds) * time.Second,
			minTraceInterval: time.Duration(minTraceIntervalSeconds) * time.Second,
			perfDuration:     time.Duration(perfDurationSeconds) * time.Second,
			threshold:        threshold,
		},
		Interval: 20,
		Flag:     tracing.FlagTracing,
	}, nil
}

type cpuUsage struct {
	system uint64
	total  uint64
}

type cpuSysTracing struct {
	interval         time.Duration
	minTraceInterval time.Duration
	perfDuration     time.Duration
	threshold        cpuSysThreshold
	lastTraceAt      time.Time
}

type cpuSysState struct {
	previousUsage      cpuUsage
	systemPercent      int64
	systemPercentDelta int64
	hasUsage           bool
	hasSystemPercent   bool
}

type cpuSysTracingData struct {
	SystemPercent               int64                  `json:"system_percent"`
	SystemPercentThreshold      int64                  `json:"system_percent_threshold"`
	SystemPercentDelta          int64                  `json:"system_percent_delta"`
	SystemPercentDeltaThreshold int64                  `json:"system_percent_delta_threshold"`
	FlameData                   []flamegraph.FrameData `json:"flamedata"`
}

type cpuSysThreshold struct {
	delta int64
	usage int64
}

func parseCPUUsage(r io.Reader) (cpuUsage, error) {
	scanner := bufio.NewScanner(r)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return cpuUsage{}, fmt.Errorf("scan cpu statistics: %w", err)
		}
		return cpuUsage{}, errors.New("cpu statistics are empty")
	}

	fields := strings.Fields(scanner.Text())
	if len(fields) < 5 {
		return cpuUsage{}, errors.New("cpu statistics require at least 4 counters")
	}
	if fields[0] != "cpu" {
		return cpuUsage{}, fmt.Errorf("unexpected cpu statistics label %q", fields[0])
	}

	// user and nice already include guest time, so guest fields must not be
	// added again when calculating the total.
	counterNames := [...]string{
		"user",
		"nice",
		"system",
		"idle",
		"iowait",
		"irq",
		"softirq",
		"steal",
	}
	counters := fields[1:]
	if len(counters) > len(counterNames) {
		counters = counters[:len(counterNames)]
	}

	var usage cpuUsage
	for i, field := range counters {
		value, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return cpuUsage{}, fmt.Errorf(
				"parse cpu %s counter %q: %w",
				counterNames[i],
				field,
				err,
			)
		}

		usage.total += value
		if i == 2 {
			usage.system = value
		}
	}

	return usage, nil
}

func readCPUUsage() (cpuUsage, error) {
	statPath := procfs.Path("stat")
	f, err := os.Open(statPath)
	if err != nil {
		return cpuUsage{}, fmt.Errorf("open %s: %w", statPath, err)
	}
	defer f.Close()

	usage, err := parseCPUUsage(f)
	if err != nil {
		return cpuUsage{}, fmt.Errorf("parse %s: %w", statPath, err)
	}
	return usage, nil
}

func (s *cpuSysState) update(usage cpuUsage) bool {
	previousUsage := s.previousUsage
	s.previousUsage = usage
	if !s.hasUsage {
		s.hasUsage = true
		return false
	}

	// Counter rollback would underflow uint64 subtraction, so restart
	// percentage tracking from the current sample.
	if usage.system < previousUsage.system || usage.total < previousUsage.total {
		s.hasSystemPercent = false
		s.systemPercent = 0
		s.systemPercentDelta = 0
		return false
	}

	systemDelta := usage.system - previousUsage.system
	totalDelta := usage.total - previousUsage.total

	// System time is part of total CPU time, so a larger delta means the
	// sampled counters are inconsistent.
	if systemDelta > totalDelta {
		s.hasSystemPercent = false
		s.systemPercent = 0
		s.systemPercentDelta = 0
		return false
	}

	// No CPU time elapsed, so this sample cannot produce a percentage.
	if totalDelta == 0 {
		return false
	}

	systemPercent := int64(100 * systemDelta / totalDelta)
	if !s.hasSystemPercent {
		s.hasSystemPercent = true
		s.systemPercent = systemPercent
		s.systemPercentDelta = 0
		return true
	}

	s.systemPercentDelta = systemPercent - s.systemPercent
	s.systemPercent = systemPercent
	return true
}

func (c *cpuSysTracing) shouldTrace(state cpuSysState, sampledAt time.Time) bool {
	exceedsThreshold := state.systemPercent > c.threshold.usage &&
		state.systemPercentDelta > c.threshold.delta
	if !exceedsThreshold {
		return false
	}

	return c.lastTraceAt.IsZero() ||
		sampledAt.Sub(c.lastTraceAt) >= c.minTraceInterval
}

func (c *cpuSysTracing) saveCPUSysTrace(
	traceTime time.Time,
	state cpuSysState,
	flameData []byte,
) error {
	tracerData := cpuSysTracingData{
		SystemPercent:               state.systemPercent,
		SystemPercentThreshold:      c.threshold.usage,
		SystemPercentDelta:          state.systemPercentDelta,
		SystemPercentDeltaThreshold: c.threshold.delta,
	}

	if err := json.Unmarshal(flameData, &tracerData.FlameData); err != nil {
		return fmt.Errorf("decode system-wide perf output: %w", err)
	}

	if err := tracing.Save(&tracing.WriteRequest{
		TracerName:    cpuSysTracerName,
		TracerTime:    traceTime,
		TracerData:    &tracerData,
		TracerRunType: tracing.TracerRunTypeAutotracing,
	}); err != nil {
		return fmt.Errorf("save cpu system trace: %w", err)
	}
	return nil
}

func (c *cpuSysTracing) Start(ctx context.Context) error {
	var state cpuSysState
	initialUsage, err := readCPUUsage()
	if err != nil {
		return err
	}
	state.update(initialUsage)

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return types.ErrExitByCancelCtx
		case sampledAt := <-ticker.C:
			usage, err := readCPUUsage()
			if err != nil {
				return err
			}
			if !state.update(usage) || !c.shouldTrace(state, sampledAt) {
				continue
			}

			traceTime := time.Now()
			log.WithField("cpu_system_percent", state.systemPercent).
				WithField("cpu_system_delta", state.systemPercentDelta).
				WithField("duration_seconds", int64(c.perfDuration/time.Second)).
				Info("starting system-wide cpu profiling")

			flameData, err := runPerfCommand(ctx, perfRequest{
				duration: c.perfDuration,
			})
			if err != nil {
				return err
			}
			if err := c.saveCPUSysTrace(traceTime, state, flameData); err != nil {
				return err
			}
			c.lastTraceAt = traceTime
		}
	}
}
