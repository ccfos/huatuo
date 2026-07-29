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
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"runtime"
	"time"

	"huatuo-bamai/internal/cgroups"
	"huatuo-bamai/internal/cgroups/stats"
	"huatuo-bamai/internal/flamegraph"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/matcher"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/pkg/tracing"
	"huatuo-bamai/pkg/types"
)

const cpuIdleTracerName = "cpuidle"

func init() {
	tracing.RegisterEventTracing(cpuIdleTracerName, newCPUIdle)
}

func newCPUIdle() (*tracing.EventTracingAttr, error) {
	cgroupReader, err := cgroups.NewManager()
	if err != nil {
		return nil, err
	}

	tracer, err := newCPUIdleTracing(cgroupReader, cfg)
	if err != nil {
		return nil, err
	}

	return &tracing.EventTracingAttr{
		TracingData: tracer,
		Interval:    20,
		Flag:        tracing.FlagTracing,
	}, nil
}

type cpuUsageBreakdown[T ~int64 | ~uint64] struct {
	user   T
	system T
	total  T
}

type cpuIdleThreshold struct {
	percent cpuUsageBreakdown[int64]
	delta   cpuUsageBreakdown[int64]
}

type cpuIdleTracing struct {
	cgroupReader     cgroups.Cgroup
	interval         time.Duration
	perfDuration     time.Duration
	minTraceInterval time.Duration
	threshold        cpuIdleThreshold
	filter           *matcher.ContainerMatcher
	containers       map[string]*containerCPUState
}

type containerCPUState struct {
	previousUsage   cpuUsageBreakdown[uint64]
	currentPercent  cpuUsageBreakdown[int64]
	previousPercent cpuUsageBreakdown[int64]
	percentDelta    cpuUsageBreakdown[int64]

	containerID  string
	cgroupPath   string
	seen         bool
	hasUsage     bool
	hasPercent   bool
	lastSampleAt time.Time
	lastTraceAt  time.Time
}

type cpuIdleTracingData struct {
	UserPercent                 int64                  `json:"user_percent"`
	UserPercentThreshold        int64                  `json:"user_percent_threshold"`
	UserPercentDelta            int64                  `json:"user_percent_delta"`
	UserPercentDeltaThreshold   int64                  `json:"user_percent_delta_threshold"`
	SystemPercent               int64                  `json:"system_percent"`
	SystemPercentThreshold      int64                  `json:"system_percent_threshold"`
	SystemPercentDelta          int64                  `json:"system_percent_delta"`
	SystemPercentDeltaThreshold int64                  `json:"system_percent_delta_threshold"`
	TotalPercent                int64                  `json:"total_percent"`
	TotalPercentThreshold       int64                  `json:"total_percent_threshold"`
	TotalPercentDelta           int64                  `json:"total_percent_delta"`
	TotalPercentDeltaThreshold  int64                  `json:"total_percent_delta_threshold"`
	FlameData                   []flamegraph.FrameData `json:"flamedata"`
}

func newCPUIdleTracing(
	cgroupReader cgroups.Cgroup,
	config *Config,
) (*cpuIdleTracing, error) {
	threshold := cpuIdleThreshold{
		percent: cpuUsageBreakdown[int64]{
			user:   config.CPUIdle.UserThreshold,
			system: config.CPUIdle.SysThreshold,
			total:  config.CPUIdle.UsageThreshold,
		},
		delta: cpuUsageBreakdown[int64]{
			user:   config.CPUIdle.DeltaUserThreshold,
			system: config.CPUIdle.DeltaSysThreshold,
			total:  config.CPUIdle.DeltaUsageThreshold,
		},
	}
	if err := validateCPUIdleConfig(
		config.CPUIdle.Interval,
		config.CPUIdle.IntervalTracing,
		config.CPUIdle.RunTracingToolTimeout,
		threshold,
	); err != nil {
		return nil, fmt.Errorf("validate container cpu config: %w", err)
	}

	filter, err := config.CPUIdle.Filter.Build()
	if err != nil {
		return nil, fmt.Errorf("build container filter: %w", err)
	}

	return &cpuIdleTracing{
		cgroupReader:     cgroupReader,
		interval:         time.Duration(config.CPUIdle.Interval) * time.Second,
		perfDuration:     time.Duration(config.CPUIdle.RunTracingToolTimeout) * time.Second,
		minTraceInterval: time.Duration(config.CPUIdle.IntervalTracing) * time.Second,
		threshold:        threshold,
		filter:           filter,
		containers:       make(map[string]*containerCPUState),
	}, nil
}

func (c *cpuIdleTracing) reconcileContainerStates(containers map[string]*pod.Container) {
	for _, state := range c.containers {
		state.seen = false
	}

	for _, container := range containers {
		if !c.filter.Match(container) {
			continue
		}

		state, ok := c.containers[container.ID]
		if !ok {
			c.containers[container.ID] = &containerCPUState{
				containerID: container.ID,
				cgroupPath:  container.CgroupPath,
				seen:        true,
			}
			continue
		}

		if state.cgroupPath != container.CgroupPath {
			state.resetMeasurements()
			state.cgroupPath = container.CgroupPath
		}
		state.seen = true
	}

	for containerID, state := range c.containers {
		if !state.seen {
			delete(c.containers, containerID)
		}
	}
}

func (s *containerCPUState) resetMeasurements() {
	s.previousUsage = cpuUsageBreakdown[uint64]{}
	s.currentPercent = cpuUsageBreakdown[int64]{}
	s.previousPercent = cpuUsageBreakdown[int64]{}
	s.percentDelta = cpuUsageBreakdown[int64]{}
	s.hasUsage = false
	s.hasPercent = false
	s.lastSampleAt = time.Time{}
}

func cpuUsageMeasurement(usage *stats.CpuUsage) cpuUsageBreakdown[uint64] {
	return cpuUsageBreakdown[uint64]{
		user:   usage.User,
		system: usage.System,
		total:  usage.Usage,
	}
}

func cpuUsageDelta(
	current cpuUsageBreakdown[uint64],
	previous cpuUsageBreakdown[uint64],
) (cpuUsageBreakdown[uint64], bool) {
	if current.user < previous.user ||
		current.system < previous.system ||
		current.total < previous.total {
		return cpuUsageBreakdown[uint64]{}, false
	}

	return cpuUsageBreakdown[uint64]{
		user:   current.user - previous.user,
		system: current.system - previous.system,
		total:  current.total - previous.total,
	}, true
}

func containerCPUCapacity(quota *stats.CpuQuota) (float64, error) {
	if quota.Quota == math.MaxUint64 {
		return float64(runtime.NumCPU()), nil
	}
	if quota.Quota == 0 {
		return 0, fmt.Errorf("container cpu quota must be positive")
	}
	if quota.Period == 0 {
		return 0, fmt.Errorf("container cpu period must be positive")
	}

	return float64(quota.Quota) / float64(quota.Period), nil
}

func (s *containerCPUState) update(
	usage cpuUsageBreakdown[uint64],
	cpuCapacity float64,
	sampledAt time.Time,
) bool {
	previousUsage := s.previousUsage
	s.previousUsage = usage
	if !s.hasUsage {
		s.hasUsage = true
		s.lastSampleAt = sampledAt
		return false
	}

	usageDelta, ok := cpuUsageDelta(usage, previousUsage)
	if !ok {
		s.resetPercentages()
		s.lastSampleAt = sampledAt
		return false
	}

	elapsed := sampledAt.Sub(s.lastSampleAt)
	s.lastSampleAt = sampledAt
	elapsedMicroseconds := elapsed.Microseconds()
	if elapsedMicroseconds <= 0 || cpuCapacity <= 0 {
		s.resetPercentages()
		return false
	}

	elapsedMicros := float64(elapsedMicroseconds)
	s.currentPercent = cpuUsageBreakdown[int64]{
		user: int64(
			float64(usageDelta.user) * 100 / elapsedMicros / cpuCapacity,
		),
		system: int64(
			float64(usageDelta.system) * 100 / elapsedMicros / cpuCapacity,
		),
		total: int64(
			float64(usageDelta.total) * 100 / elapsedMicros / cpuCapacity,
		),
	}
	if !s.hasPercent {
		s.previousPercent = s.currentPercent
		s.percentDelta = cpuUsageBreakdown[int64]{}
		s.hasPercent = true
		return true
	}

	s.percentDelta = cpuUsageBreakdown[int64]{
		user:   s.currentPercent.user - s.previousPercent.user,
		system: s.currentPercent.system - s.previousPercent.system,
		total:  s.currentPercent.total - s.previousPercent.total,
	}
	s.previousPercent = s.currentPercent

	return true
}

func (s *containerCPUState) resetPercentages() {
	s.currentPercent = cpuUsageBreakdown[int64]{}
	s.previousPercent = cpuUsageBreakdown[int64]{}
	s.percentDelta = cpuUsageBreakdown[int64]{}
	s.hasPercent = false
}

func (s *containerCPUState) traceScore(threshold cpuIdleThreshold) (int64, bool) {
	var score int64
	var exceedsThreshold bool
	if s.currentPercent.user > threshold.percent.user &&
		s.percentDelta.user > threshold.delta.user {
		score = s.currentPercent.user - threshold.percent.user +
			s.percentDelta.user - threshold.delta.user
		exceedsThreshold = true
	}
	if s.currentPercent.system > threshold.percent.system &&
		s.percentDelta.system > threshold.delta.system {
		systemScore := s.currentPercent.system - threshold.percent.system +
			s.percentDelta.system - threshold.delta.system
		score = max(score, systemScore)
		exceedsThreshold = true
	}
	if s.currentPercent.total > threshold.percent.total &&
		s.percentDelta.total > threshold.delta.total {
		totalScore := s.currentPercent.total - threshold.percent.total +
			s.percentDelta.total - threshold.delta.total
		score = max(score, totalScore)
		exceedsThreshold = true
	}

	return score, exceedsThreshold
}

func (c *cpuIdleTracing) readContainerCPUSample(
	state *containerCPUState,
) (cpuUsageBreakdown[uint64], float64, error) {
	quota, err := c.cgroupReader.CpuQuotaAndPeriod(state.cgroupPath)
	if err != nil {
		return cpuUsageBreakdown[uint64]{}, 0,
			fmt.Errorf("read cpu quota for %q: %w", state.containerID, err)
	}
	capacity, err := containerCPUCapacity(quota)
	if err != nil {
		return cpuUsageBreakdown[uint64]{}, 0,
			fmt.Errorf("calculate cpu capacity for %q: %w", state.containerID, err)
	}

	usage, err := c.cgroupReader.CpuUsage(state.cgroupPath)
	if err != nil {
		return cpuUsageBreakdown[uint64]{}, 0,
			fmt.Errorf("read cpu usage for %q: %w", state.containerID, err)
	}

	return cpuUsageMeasurement(usage), capacity, nil
}

func (c *cpuIdleTracing) updateContainerCPUStates(sampledAt time.Time) error {
	for _, state := range c.containers {
		usage, capacity, err := c.readContainerCPUSample(state)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}

			log.WithError(err).
				WithField("container_id", state.containerID).
				WithField("cgroup_path", state.cgroupPath).
				Debug("failed to sample container cpu")
			continue
		}
		state.update(usage, capacity, sampledAt)
	}

	return nil
}

func (c *cpuIdleTracing) selectTraceTarget(sampledAt time.Time) *containerCPUState {
	var traceTarget *containerCPUState
	var highestScore int64

	for _, state := range c.containers {
		if !state.hasPercent || !state.lastSampleAt.Equal(sampledAt) {
			continue
		}
		if !state.lastTraceAt.IsZero() &&
			sampledAt.Sub(state.lastTraceAt) < c.minTraceInterval {
			continue
		}

		score, exceedsThreshold := state.traceScore(c.threshold)
		if !exceedsThreshold {
			continue
		}
		if traceTarget == nil ||
			score > highestScore ||
			(score == highestScore && state.containerID < traceTarget.containerID) {
			traceTarget = state
			highestScore = score
		}
	}

	return traceTarget
}

func (c *cpuIdleTracing) saveCPUIdleTrace(
	state *containerCPUState,
	traceTime time.Time,
	flameData []byte,
) error {
	tracerData := cpuIdleTracingData{
		UserPercent:                 state.currentPercent.user,
		UserPercentThreshold:        c.threshold.percent.user,
		UserPercentDelta:            state.percentDelta.user,
		UserPercentDeltaThreshold:   c.threshold.delta.user,
		SystemPercent:               state.currentPercent.system,
		SystemPercentThreshold:      c.threshold.percent.system,
		SystemPercentDelta:          state.percentDelta.system,
		SystemPercentDeltaThreshold: c.threshold.delta.system,
		TotalPercent:                state.currentPercent.total,
		TotalPercentThreshold:       c.threshold.percent.total,
		TotalPercentDelta:           state.percentDelta.total,
		TotalPercentDeltaThreshold:  c.threshold.delta.total,
	}
	if err := json.Unmarshal(flameData, &tracerData.FlameData); err != nil {
		return fmt.Errorf("decode container perf output: %w", err)
	}

	if err := tracing.Save(&tracing.WriteRequest{
		TracerName:    cpuIdleTracerName,
		ContainerID:   state.containerID,
		TracerTime:    traceTime,
		TracerData:    &tracerData,
		TracerRunType: tracing.TracerRunTypeAutotracing,
	}); err != nil {
		return fmt.Errorf("save container cpu trace: %w", err)
	}

	return nil
}

func (c *cpuIdleTracing) Start(ctx context.Context) error {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return types.ErrExitByCancelCtx
		case sampledAt := <-ticker.C:
			containers, err := pod.NormalContainers()
			if err != nil {
				return fmt.Errorf("list containers for cpu sampling: %w", err)
			}
			c.reconcileContainerStates(containers)

			if err := c.updateContainerCPUStates(sampledAt); err != nil {
				return err
			}
			traceTarget := c.selectTraceTarget(sampledAt)
			if traceTarget == nil {
				continue
			}

			traceTime := time.Now()
			log.WithField("container_id", traceTarget.containerID).
				WithField("cgroup_path", traceTarget.cgroupPath).
				WithField("cpu_percent", traceTarget.currentPercent).
				WithField("cpu_percent_delta", traceTarget.percentDelta).
				WithField("duration_seconds", int64(c.perfDuration/time.Second)).
				Info("starting container cpu profiling")

			flameData, err := runPerfCommand(ctx, perfRequest{
				duration:    c.perfDuration,
				containerID: traceTarget.containerID,
			})
			if err != nil {
				return err
			}
			if err := c.saveCPUIdleTrace(traceTarget, traceTime, flameData); err != nil {
				return err
			}
			traceTarget.lastTraceAt = traceTime
		}
	}
}
