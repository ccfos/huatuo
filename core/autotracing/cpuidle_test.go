// Copyright 2026 The HuaTuo Authors
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
	"encoding/json"
	"errors"
	"math"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"huatuo-bamai/internal/cgroups"
	"huatuo-bamai/internal/cgroups/stats"
	"huatuo-bamai/internal/pod"
)

type stubContainerCPUReader struct {
	cgroups.Cgroup

	usage    map[string]stats.CpuUsage
	quota    map[string]stats.CpuQuota
	usageErr map[string]error
	quotaErr map[string]error
}

func (r *stubContainerCPUReader) CpuUsage(path string) (*stats.CpuUsage, error) {
	if err := r.usageErr[path]; err != nil {
		return nil, err
	}

	usage := r.usage[path]
	return &usage, nil
}

func (r *stubContainerCPUReader) CpuQuotaAndPeriod(path string) (*stats.CpuQuota, error) {
	if err := r.quotaErr[path]; err != nil {
		return nil, err
	}

	quota := r.quota[path]
	return &quota, nil
}

func TestNewCPUIdleTracingBindsConfig(t *testing.T) {
	t.Parallel()

	config := &Config{}
	config.CPUIdle.UserThreshold = 75
	config.CPUIdle.SysThreshold = 45
	config.CPUIdle.UsageThreshold = 90
	config.CPUIdle.DeltaUserThreshold = 40
	config.CPUIdle.DeltaSysThreshold = 20
	config.CPUIdle.DeltaUsageThreshold = 50
	config.CPUIdle.Interval = 12
	config.CPUIdle.IntervalTracing = 300
	config.CPUIdle.RunTracingToolTimeout = 7

	tracer, err := newCPUIdleTracing(&stubContainerCPUReader{}, config)
	if err != nil {
		t.Fatalf("newCPUIdleTracing() error = %v", err)
	}
	if tracer.interval != 12*time.Second {
		t.Errorf("interval = %s, want 12s", tracer.interval)
	}
	if tracer.minTraceInterval != 300*time.Second {
		t.Errorf("minTraceInterval = %s, want 5m0s", tracer.minTraceInterval)
	}
	if tracer.perfDuration != 7*time.Second {
		t.Errorf("perfDuration = %s, want 7s", tracer.perfDuration)
	}
	if tracer.threshold.percent != (cpuUsageBreakdown[int64]{user: 75, system: 45, total: 90}) {
		t.Errorf("percent threshold = %+v, want user=75 system=45 total=90", tracer.threshold.percent)
	}
	if tracer.threshold.delta != (cpuUsageBreakdown[int64]{user: 40, system: 20, total: 50}) {
		t.Errorf("delta threshold = %+v, want user=40 system=20 total=50", tracer.threshold.delta)
	}
	if tracer.containers == nil {
		t.Error("containers = nil, want initialized map")
	}
}

func TestValidateCPUIdleConfig(t *testing.T) {
	t.Parallel()

	validThreshold := cpuIdleThreshold{
		percent: cpuUsageBreakdown[int64]{user: 75, system: 45, total: 90},
		delta:   cpuUsageBreakdown[int64]{user: 40, system: 20, total: 50},
	}
	tests := []struct {
		name      string
		config    cpuTracingConfig
		threshold cpuIdleThreshold
		wantError string
	}{
		{
			name: "valid",
			config: cpuTracingConfig{
				intervalSeconds:         10,
				minTraceIntervalSeconds: 1800,
				perfDurationSeconds:     10,
				systemThreshold:         45,
				systemDeltaThreshold:    20,
			},
			threshold: validThreshold,
		},
		{
			name: "negative user threshold",
			config: cpuTracingConfig{
				intervalSeconds:         10,
				minTraceIntervalSeconds: 1800,
				perfDurationSeconds:     10,
			},
			threshold: cpuIdleThreshold{
				percent: cpuUsageBreakdown[int64]{user: -1},
			},
			wantError: "user threshold: cpu percentage must be between 0 and 100",
		},
		{
			name: "total delta threshold above maximum",
			config: cpuTracingConfig{
				intervalSeconds:         10,
				minTraceIntervalSeconds: 1800,
				perfDurationSeconds:     10,
			},
			threshold: cpuIdleThreshold{
				delta: cpuUsageBreakdown[int64]{total: 101},
			},
			wantError: "total delta threshold: cpu percentage must be between 0 and 100",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateCPUIdleConfig(
				tt.config.intervalSeconds,
				tt.config.minTraceIntervalSeconds,
				tt.config.perfDurationSeconds,
				tt.threshold,
			)
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("validateCPUIdleConfig() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("validateCPUIdleConfig() error = %v, want contain %q", err, tt.wantError)
			}
		})
	}
}

func TestContainerCPUCapacity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		quota     stats.CpuQuota
		want      float64
		wantError string
	}{
		{
			name:  "half cpu",
			quota: stats.CpuQuota{Quota: 50_000, Period: 100_000},
			want:  0.5,
		},
		{
			name:  "one and a half cpus",
			quota: stats.CpuQuota{Quota: 150_000, Period: 100_000},
			want:  1.5,
		},
		{
			name:  "unlimited",
			quota: stats.CpuQuota{Quota: math.MaxUint64},
			want:  float64(runtime.NumCPU()),
		},
		{
			name:      "zero quota",
			quota:     stats.CpuQuota{Period: 100_000},
			wantError: "quota must be positive",
		},
		{
			name:      "zero period",
			quota:     stats.CpuQuota{Quota: 100_000},
			wantError: "period must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual, err := containerCPUCapacity(&tt.quota)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("containerCPUCapacity() error = %v, want contain %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("containerCPUCapacity() error = %v", err)
			}
			if actual != tt.want {
				t.Errorf("containerCPUCapacity() = %v, want %v", actual, tt.want)
			}
		})
	}
}

func TestContainerCPUStateUpdate(t *testing.T) {
	t.Parallel()

	sampledAt := time.Unix(100, 0)
	var state containerCPUState
	if state.update(cpuUsageBreakdown[uint64]{}, 0.5, sampledAt) {
		t.Fatal("first update = true, want false")
	}

	sampledAt = sampledAt.Add(time.Second)
	if !state.update(
		cpuUsageBreakdown[uint64]{user: 250_000, system: 100_000, total: 500_000},
		0.5,
		sampledAt,
	) {
		t.Fatal("second update = false, want true")
	}
	wantPercent := cpuUsageBreakdown[int64]{user: 50, system: 20, total: 100}
	if state.currentPercent != wantPercent {
		t.Errorf("currentPercent = %+v, want %+v", state.currentPercent, wantPercent)
	}
	if state.percentDelta != (cpuUsageBreakdown[int64]{}) {
		t.Errorf("first percentDelta = %+v, want zero", state.percentDelta)
	}

	sampledAt = sampledAt.Add(time.Second)
	if !state.update(
		cpuUsageBreakdown[uint64]{user: 300_000, system: 100_000, total: 600_000},
		0.5,
		sampledAt,
	) {
		t.Fatal("third update = false, want true")
	}
	wantPercent = cpuUsageBreakdown[int64]{user: 10, total: 20}
	if state.currentPercent != wantPercent {
		t.Errorf("currentPercent = %+v, want %+v", state.currentPercent, wantPercent)
	}
	wantDelta := cpuUsageBreakdown[int64]{user: -40, system: -20, total: -80}
	if state.percentDelta != wantDelta {
		t.Errorf("percentDelta = %+v, want %+v", state.percentDelta, wantDelta)
	}
}

func TestContainerCPUStateUpdateResetsInvalidSamples(t *testing.T) {
	t.Parallel()

	start := time.Unix(100, 0)
	tests := []struct {
		name     string
		first    cpuUsageBreakdown[uint64]
		second   cpuUsageBreakdown[uint64]
		secondAt time.Time
	}{
		{
			name:     "counter rollback",
			first:    cpuUsageBreakdown[uint64]{user: 100, system: 50, total: 200},
			second:   cpuUsageBreakdown[uint64]{user: 10, system: 5, total: 20},
			secondAt: start.Add(time.Second),
		},
		{
			name:     "zero elapsed time",
			first:    cpuUsageBreakdown[uint64]{},
			second:   cpuUsageBreakdown[uint64]{user: 10, total: 10},
			secondAt: start,
		},
		{
			name:     "sub-microsecond elapsed time",
			first:    cpuUsageBreakdown[uint64]{},
			second:   cpuUsageBreakdown[uint64]{user: 10, total: 10},
			secondAt: start.Add(time.Nanosecond),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			state := containerCPUState{
				currentPercent:  cpuUsageBreakdown[int64]{user: 70, total: 70},
				previousPercent: cpuUsageBreakdown[int64]{user: 60, total: 60},
				percentDelta:    cpuUsageBreakdown[int64]{user: 10, total: 10},
				hasPercent:      true,
			}
			state.update(tt.first, 1, start)
			if state.update(tt.second, 1, tt.secondAt) {
				t.Fatal("invalid update = true, want false")
			}
			if state.hasPercent {
				t.Error("hasPercent = true, want false")
			}
			if state.previousUsage != tt.second {
				t.Errorf("previousUsage = %+v, want %+v", state.previousUsage, tt.second)
			}
		})
	}
}

func TestContainerCPUStateTraceScore(t *testing.T) {
	t.Parallel()

	threshold := cpuIdleThreshold{
		percent: cpuUsageBreakdown[int64]{user: 75, system: 45, total: 90},
		delta:   cpuUsageBreakdown[int64]{user: 40, system: 20, total: 50},
	}
	state := containerCPUState{
		currentPercent: cpuUsageBreakdown[int64]{user: 80},
		percentDelta:   cpuUsageBreakdown[int64]{user: 45},
	}
	score, exceedsThreshold := state.traceScore(threshold)
	if !exceedsThreshold {
		t.Fatal("traceScore() exceedsThreshold = false, want true")
	}
	if score != 10 {
		t.Errorf("traceScore() score = %d, want 10", score)
	}
}

func TestCPUIdleTracingReconcileContainerStates(t *testing.T) {
	t.Parallel()

	traceTime := time.Unix(100, 0)
	tracer := &cpuIdleTracing{
		containers: map[string]*containerCPUState{
			"keep": {
				containerID:  "keep",
				cgroupPath:   "old",
				hasUsage:     true,
				hasPercent:   true,
				lastTraceAt:  traceTime,
				lastSampleAt: traceTime,
			},
			"remove": {
				containerID: "remove",
				cgroupPath:  "remove",
			},
		},
	}
	tracer.reconcileContainerStates(map[string]*pod.Container{
		"keep": {
			ID:         "keep",
			CgroupPath: "new",
		},
	})

	if len(tracer.containers) != 1 {
		t.Fatalf("len(containers) = %d, want 1", len(tracer.containers))
	}
	state := tracer.containers["keep"]
	if state == nil {
		t.Fatal(`containers["keep"] = nil, want state`)
	}
	if state.cgroupPath != "new" {
		t.Errorf("cgroupPath = %q, want %q", state.cgroupPath, "new")
	}
	if state.hasUsage || state.hasPercent {
		t.Errorf("measurement state = hasUsage:%t hasPercent:%t, want reset", state.hasUsage, state.hasPercent)
	}
	if state.lastTraceAt != traceTime {
		t.Errorf("lastTraceAt = %s, want %s", state.lastTraceAt, traceTime)
	}
}

func TestCPUIdleTracingSelectTraceTarget(t *testing.T) {
	t.Parallel()

	reader := &stubContainerCPUReader{
		usage: map[string]stats.CpuUsage{
			"a": {},
			"b": {},
		},
		quota: map[string]stats.CpuQuota{
			"a": {Quota: 100_000, Period: 100_000},
			"b": {Quota: 100_000, Period: 100_000},
		},
	}
	tracer := &cpuIdleTracing{
		cgroupReader:     reader,
		minTraceInterval: 30 * time.Minute,
		threshold: cpuIdleThreshold{
			percent: cpuUsageBreakdown[int64]{user: 50, total: 50},
			delta:   cpuUsageBreakdown[int64]{user: 20, total: 20},
		},
		containers: map[string]*containerCPUState{
			"a": {containerID: "a", cgroupPath: "a"},
			"b": {containerID: "b", cgroupPath: "b"},
		},
	}

	sampledAt := time.Unix(100, 0)
	if err := tracer.updateContainerCPUStates(sampledAt); err != nil {
		t.Fatalf("updateContainerCPUStates(first) error = %v", err)
	}
	traceTarget := tracer.selectTraceTarget(sampledAt)
	if traceTarget != nil {
		t.Fatalf("first trace target = %q, want nil", traceTarget.containerID)
	}

	reader.usage["a"] = stats.CpuUsage{User: 100_000, Usage: 100_000}
	reader.usage["b"] = stats.CpuUsage{User: 100_000, Usage: 100_000}
	sampledAt = sampledAt.Add(time.Second)
	if err := tracer.updateContainerCPUStates(sampledAt); err != nil {
		t.Fatalf("updateContainerCPUStates(second) error = %v", err)
	}
	traceTarget = tracer.selectTraceTarget(sampledAt)
	if traceTarget != nil {
		t.Fatalf("second trace target = %q, want nil", traceTarget.containerID)
	}

	reader.usage["a"] = stats.CpuUsage{User: 900_000, Usage: 900_000}
	reader.usage["b"] = stats.CpuUsage{User: 1_000_000, Usage: 1_000_000}
	sampledAt = sampledAt.Add(time.Second)
	if err := tracer.updateContainerCPUStates(sampledAt); err != nil {
		t.Fatalf("updateContainerCPUStates(third) error = %v", err)
	}
	traceTarget = tracer.selectTraceTarget(sampledAt)
	if traceTarget == nil {
		t.Fatal("third trace target = nil, want b")
	}
	if traceTarget.containerID != "b" {
		t.Errorf("third trace target = %q, want %q", traceTarget.containerID, "b")
	}
	if tracer.containers["a"].currentPercent.total != 80 {
		t.Errorf(
			`containers["a"].currentPercent.total = %d, want 80`,
			tracer.containers["a"].currentPercent.total,
		)
	}
}

func TestCPUIdleTracingSelectTraceTargetHonorsMinTraceInterval(t *testing.T) {
	t.Parallel()

	lastTraceAt := time.Unix(100, 0)
	state := &containerCPUState{
		containerID:    "container",
		currentPercent: cpuUsageBreakdown[int64]{user: 80},
		percentDelta:   cpuUsageBreakdown[int64]{user: 45},
		hasPercent:     true,
		lastTraceAt:    lastTraceAt,
	}
	tracer := &cpuIdleTracing{
		minTraceInterval: 30 * time.Minute,
		threshold: cpuIdleThreshold{
			percent: cpuUsageBreakdown[int64]{user: 75},
			delta:   cpuUsageBreakdown[int64]{user: 40},
		},
		containers: map[string]*containerCPUState{
			state.containerID: state,
		},
	}

	sampledAt := lastTraceAt.Add(29 * time.Minute)
	state.lastSampleAt = sampledAt
	if traceTarget := tracer.selectTraceTarget(sampledAt); traceTarget != nil {
		t.Errorf(
			"selectTraceTarget(before minimum interval) = %q, want nil",
			traceTarget.containerID,
		)
	}

	sampledAt = lastTraceAt.Add(30 * time.Minute)
	state.lastSampleAt = sampledAt
	if traceTarget := tracer.selectTraceTarget(sampledAt); traceTarget != state {
		t.Errorf("selectTraceTarget(at minimum interval) = %p, want %p", traceTarget, state)
	}
}

func TestCPUIdleTracingUpdateContainerCPUStatesErrors(t *testing.T) {
	t.Parallel()

	previousSampleAt := time.Unix(99, 0)
	reader := &stubContainerCPUReader{
		quotaErr: map[string]error{
			"deleted": os.ErrNotExist,
		},
	}
	tracer := &cpuIdleTracing{
		cgroupReader: reader,
		containers: map[string]*containerCPUState{
			"deleted": {
				containerID:    "deleted",
				cgroupPath:     "deleted",
				currentPercent: cpuUsageBreakdown[int64]{user: 80},
				percentDelta:   cpuUsageBreakdown[int64]{user: 45},
				hasPercent:     true,
				lastSampleAt:   previousSampleAt,
			},
		},
	}

	sampledAt := time.Unix(100, 0)
	if err := tracer.updateContainerCPUStates(sampledAt); err != nil {
		t.Fatalf("updateContainerCPUStates(deleted) error = %v", err)
	}
	if traceTarget := tracer.selectTraceTarget(sampledAt); traceTarget != nil {
		t.Errorf("selectTraceTarget(deleted) = %q, want nil", traceTarget.containerID)
	}

	readErr := errors.New("permission denied")
	reader.quotaErr["deleted"] = readErr
	err := tracer.updateContainerCPUStates(time.Unix(101, 0))
	if !errors.Is(err, readErr) {
		t.Fatalf("updateContainerCPUStates(failed) error = %v, want %v", err, readErr)
	}
}

func TestCPUIdleTracingDataJSON(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(cpuIdleTracingData{
		UserPercent:                 80,
		UserPercentThreshold:        75,
		UserPercentDelta:            45,
		UserPercentDeltaThreshold:   40,
		SystemPercent:               20,
		SystemPercentThreshold:      45,
		SystemPercentDelta:          5,
		SystemPercentDeltaThreshold: 20,
		TotalPercent:                95,
		TotalPercentThreshold:       90,
		TotalPercentDelta:           55,
		TotalPercentDeltaThreshold:  50,
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var actual map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &actual); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	expected := map[string]string{
		"user_percent":                   "80",
		"user_percent_threshold":         "75",
		"user_percent_delta":             "45",
		"user_percent_delta_threshold":   "40",
		"system_percent":                 "20",
		"system_percent_threshold":       "45",
		"system_percent_delta":           "5",
		"system_percent_delta_threshold": "20",
		"total_percent":                  "95",
		"total_percent_threshold":        "90",
		"total_percent_delta":            "55",
		"total_percent_delta_threshold":  "50",
		"flamedata":                      "null",
	}
	if len(actual) != len(expected) {
		t.Fatalf("JSON fields = %v, want %v", actual, expected)
	}
	for field, expectedValue := range expected {
		if actualValue, ok := actual[field]; !ok || string(actualValue) != expectedValue {
			t.Errorf("JSON field %q = %s, want %s", field, actualValue, expectedValue)
		}
	}
}

func TestSaveCPUIdleTraceRejectsInvalidPerfOutput(t *testing.T) {
	t.Parallel()

	tracer := &cpuIdleTracing{}
	err := tracer.saveCPUIdleTrace(
		&containerCPUState{},
		time.Unix(100, 0),
		[]byte("not-json"),
	)
	if err == nil || !strings.Contains(err.Error(), "decode container perf output") {
		t.Fatalf("saveCPUIdleTrace() error = %v, want decode error", err)
	}
}
