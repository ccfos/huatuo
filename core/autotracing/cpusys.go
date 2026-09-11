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

	"github.com/ccfos/huatuo/internal/flamegraph"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/types"
)

const (
	cpuSysTracerName = "cpusys"
)

func init() {
	tracing.RegisterEventTracing(cpuSysTracerName, newCPUSys)
}

func newCPUSys() (*tracing.EventTracingAttr, error) {
	cfg := configSnapshot()
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
	if err := cfg.validateCPUSysAdditionalThresholds(); err != nil {
		return nil, err
	}

	return &tracing.EventTracingAttr{
		TracingData: &cpuSysTracing{
			interval:         time.Duration(intervalSeconds) * time.Second,
			minTraceInterval: time.Duration(minTraceIntervalSeconds) * time.Second,
			perfDuration:     time.Duration(perfDurationSeconds) * time.Second,
			threshold:        threshold,
			userThreshold:    cpuSysThreshold{usage: cfg.CPUSys.UserThreshold, delta: cfg.CPUSys.DeltaUserThreshold},
			totalThreshold:   cpuSysThreshold{usage: cfg.CPUSys.UsageThreshold, delta: cfg.CPUSys.DeltaUsageThreshold},
		},
		Interval: 20,
		Flag:     tracing.FlagTracing,
	}, nil
}

type cpuUsage struct {
	system uint64
	total  uint64
	user   uint64
	busy   uint64
}

type cpuSysTracing struct {
	interval         time.Duration
	minTraceInterval time.Duration
	perfDuration     time.Duration
	threshold        cpuSysThreshold
	lastTraceAt      time.Time
	userThreshold    cpuSysThreshold
	totalThreshold   cpuSysThreshold
}

type cpuSysState struct {
	previousUsage      cpuUsage
	systemPercent      int64
	systemPercentDelta int64
	hasUsage           bool
	hasSystemPercent   bool
	userPercent        int64
	userPercentDelta   int64
	totalPercent       int64
	totalPercentDelta  int64
}

type cpuSysTracingData struct {
	SystemPercent               int64                  `json:"system_percent"`
	SystemPercentThreshold      int64                  `json:"system_percent_threshold"`
	SystemPercentDelta          int64                  `json:"system_percent_delta"`
	SystemPercentDeltaThreshold int64                  `json:"system_percent_delta_threshold"`
	FlameData                   []flamegraph.FrameData `json:"flamedata"`
	UserPercent                 int64                  `json:"user_percent,omitempty"`
	UserPercentThreshold        int64                  `json:"user_percent_threshold,omitempty"`
	UserPercentDelta            int64                  `json:"user_percent_delta,omitempty"`
	UserPercentDeltaThreshold   int64                  `json:"user_percent_delta_threshold,omitempty"`
	TotalPercent                int64                  `json:"total_percent,omitempty"`
	TotalPercentThreshold       int64                  `json:"total_percent_threshold,omitempty"`
	TotalPercentDelta           int64                  `json:"total_percent_delta,omitempty"`
	TotalPercentDeltaThreshold  int64                  `json:"total_percent_delta_threshold,omitempty"`
	TriggerReasons              []string               `json:"trigger_reasons,omitempty"`
}

type cpuSysThreshold struct {
	delta int64
	usage int64
}

func (t cpuSysThreshold) additionalTrigger(percent, delta int64) bool {
	// Omitted user/total thresholds must preserve legacy system-only triggering.
	return t.usage > 0 && percent > t.usage && delta > t.delta
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
		if i < 2 {
			usage.user += value
		}
		// Waiting and stolen time cannot be explained by a local CPU profile.
		if i < 3 || i == 5 || i == 6 {
			usage.busy += value
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
	if usage.system < previousUsage.system || usage.total < previousUsage.total ||
		usage.user < previousUsage.user || usage.busy < previousUsage.busy {
		*s = cpuSysState{previousUsage: usage, hasUsage: true}
		return false
	}

	systemDelta := usage.system - previousUsage.system
	totalDelta := usage.total - previousUsage.total
	userDelta := usage.user - previousUsage.user
	busyDelta := usage.busy - previousUsage.busy

	// System time is part of total CPU time, so a larger delta means the
	// sampled counters are inconsistent.
	if systemDelta > totalDelta || userDelta > totalDelta || busyDelta > totalDelta {
		*s = cpuSysState{previousUsage: usage, hasUsage: true}
		return false
	}

	// No CPU time elapsed, so this sample cannot produce a percentage.
	if totalDelta == 0 {
		return false
	}

	systemPercent := int64(100 * systemDelta / totalDelta)
	userPercent := int64(100 * userDelta / totalDelta)
	totalPercent := int64(100 * busyDelta / totalDelta)
	if !s.hasSystemPercent {
		s.hasSystemPercent = true
		s.systemPercent = systemPercent
		s.systemPercentDelta = 0
		s.userPercent = userPercent
		s.totalPercent = totalPercent
		return true
	}

	s.systemPercentDelta = systemPercent - s.systemPercent
	s.systemPercent = systemPercent
	s.userPercentDelta = userPercent - s.userPercent
	s.userPercent = userPercent
	s.totalPercentDelta = totalPercent - s.totalPercent
	s.totalPercent = totalPercent
	return true
}

func (c *cpuSysTracing) shouldTrace(state *cpuSysState, sampledAt time.Time) bool {
	exceedsThreshold := state.systemPercent > c.threshold.usage &&
		state.systemPercentDelta > c.threshold.delta
	exceedsThreshold = exceedsThreshold || c.userThreshold.additionalTrigger(state.userPercent, state.userPercentDelta) ||
		c.totalThreshold.additionalTrigger(state.totalPercent, state.totalPercentDelta)
	if !exceedsThreshold {
		return false
	}

	return c.lastTraceAt.IsZero() ||
		sampledAt.Sub(c.lastTraceAt) >= c.minTraceInterval
}

func (c *cpuSysTracing) saveCPUSysTrace(
	traceTime time.Time,
	state *cpuSysState,
	flameData []byte,
) error {
	tracerData := cpuSysTracingData{
		SystemPercent:               state.systemPercent,
		SystemPercentThreshold:      c.threshold.usage,
		SystemPercentDelta:          state.systemPercentDelta,
		SystemPercentDeltaThreshold: c.threshold.delta,
	}
	tracerData.UserPercent = state.userPercent
	tracerData.UserPercentThreshold = c.userThreshold.usage
	tracerData.UserPercentDelta = state.userPercentDelta
	tracerData.UserPercentDeltaThreshold = c.userThreshold.delta
	if c.userThreshold.additionalTrigger(state.userPercent, state.userPercentDelta) {
		tracerData.TriggerReasons = append(tracerData.TriggerReasons, "user")
	}
	tracerData.TotalPercent = state.totalPercent
	tracerData.TotalPercentThreshold = c.totalThreshold.usage
	tracerData.TotalPercentDelta = state.totalPercentDelta
	tracerData.TotalPercentDeltaThreshold = c.totalThreshold.delta
	if c.totalThreshold.additionalTrigger(state.totalPercent, state.totalPercentDelta) {
		tracerData.TriggerReasons = append(tracerData.TriggerReasons, "total")
	}
	if state.systemPercent > c.threshold.usage &&
		state.systemPercentDelta > c.threshold.delta {
		tracerData.TriggerReasons = append(tracerData.TriggerReasons, "system")
	}

	if err := json.Unmarshal(flameData, &tracerData.FlameData); err != nil {
		return fmt.Errorf("decode system-wide perf output: %w", err)
	}

	if err := tracing.Save(&tracing.WriteRequest{
		TracerName:       cpuSysTracerName,
		StartedTimestamp: traceTime,
		TracerData:       &tracerData,
		TracerRunType:    types.TracerRunTypeAutotracing,
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
			if !state.update(usage) || !c.shouldTrace(&state, sampledAt) {
				continue
			}

			traceTime := time.Now()
			log.WithField("cpu_system_percent", state.systemPercent).
				WithField("cpu_system_delta", state.systemPercentDelta).
				WithField("cpu_user_percent", state.userPercent).
				WithField("cpu_total_percent", state.totalPercent).
				WithField("duration_seconds", int64(c.perfDuration/time.Second)).
				Info("starting system-wide cpu profiling")

			flameData, err := runPerfCommand(ctx, perfRequest{
				duration: c.perfDuration,
			})
			if err != nil {
				return err
			}
			if err := c.saveCPUSysTrace(traceTime, &state, flameData); err != nil {
				return err
			}
			c.lastTraceAt = traceTime
		}
	}
}
