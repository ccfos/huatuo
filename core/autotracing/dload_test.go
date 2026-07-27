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
	"testing"
	"time"

	"huatuo-bamai/internal/pod"
)

func TestNewDloadTracing(t *testing.T) {
	validConfig := &Config{}
	validConfig.Dload.Interval = 10
	validConfig.Dload.IntervalTracing = 30
	validConfig.Dload.ThresholdLoad = 5

	tests := []struct {
		name        string
		update      func(*Config)
		expectedErr string
	}{
		{
			name: "valid config",
		},
		{
			name: "non-positive sampling interval",
			update: func(config *Config) {
				config.Dload.Interval = 0
			},
			expectedErr: "dload sampling interval must be positive",
		},
		{
			name: "negative tracing interval",
			update: func(config *Config) {
				config.Dload.IntervalTracing = -1
			},
			expectedErr: "dload tracing interval must be non-negative",
		},
		{
			name: "negative load threshold",
			update: func(config *Config) {
				config.Dload.ThresholdLoad = -1
			},
			expectedErr: "dload threshold must be non-negative",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := *validConfig
			if test.update != nil {
				test.update(&config)
			}

			tracer, err := newDloadTracing(&config)
			if test.expectedErr != "" {
				if err == nil || err.Error() != test.expectedErr {
					t.Fatalf("newDloadTracing() error = %v, want %q", err, test.expectedErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("newDloadTracing() error = %v", err)
			}
			if tracer.containers == nil {
				t.Fatal("newDloadTracing() returned a nil container map")
			}
		})
	}
}

func TestDloadTracingStateIsPerInstance(t *testing.T) {
	config := &Config{}
	config.Dload.Interval = 10

	first, err := newDloadTracing(config)
	if err != nil {
		t.Fatalf("newDloadTracing() error = %v", err)
	}
	second, err := newDloadTracing(config)
	if err != nil {
		t.Fatalf("newDloadTracing() error = %v", err)
	}

	first.containers["container"] = &containerDloadInfo{}
	if len(second.containers) != 0 {
		t.Fatalf("second tracer has %d containers, want 0", len(second.containers))
	}
}

func TestDloadTracingReconcileContainers(t *testing.T) {
	lastTraceAt := time.Unix(100, 0)
	tracer := &dloadTracing{
		containers: map[string]*containerDloadInfo{
			"existing": {
				cgroupName:  "old-path",
				lastTraceAt: lastTraceAt,
				isSeen:      true,
			},
			"removed": {
				isSeen: true,
			},
		},
	}
	existing := &pod.Container{
		ID:         "existing",
		CgroupPath: "new-path",
	}
	added := &pod.Container{
		ID:         "added",
		CgroupPath: "added-path",
	}

	tracer.reconcileContainers(map[string]*pod.Container{
		existing.ID: existing,
		added.ID:    added,
	})

	if len(tracer.containers) != 2 {
		t.Fatalf("container count = %d, want 2", len(tracer.containers))
	}
	if _, ok := tracer.containers["removed"]; ok {
		t.Fatal("removed container remains in tracer state")
	}
	existingInfo := tracer.containers["existing"]
	if existingInfo.container != existing || existingInfo.cgroupName != "new-path" {
		t.Fatalf("existing container was not refreshed: %+v", existingInfo)
	}
	if existingInfo.lastTraceAt != lastTraceAt {
		t.Fatalf("lastTraceAt = %v, want %v", existingInfo.lastTraceAt, lastTraceAt)
	}
	if addedInfo := tracer.containers["added"]; addedInfo == nil || addedInfo.container != added {
		t.Fatalf("added container state = %+v", addedInfo)
	}
}

func TestDloadTracingShouldTrace(t *testing.T) {
	sampledAt := time.Unix(100, 0)

	tests := []struct {
		name        string
		threshold   dloadThreshold
		load        float64
		lastTraceAt time.Time
		expected    bool
	}{
		{
			name: "threshold exceeded after interval",
			threshold: dloadThreshold{
				load:             5,
				minTraceInterval: 10 * time.Second,
			},
			load:        6,
			lastTraceAt: sampledAt.Add(-10 * time.Second),
			expected:    true,
		},
		{
			name: "threshold boundary is not exceeded",
			threshold: dloadThreshold{
				load: 5,
			},
			load:        5,
			lastTraceAt: sampledAt.Add(-time.Hour),
		},
		{
			name: "minimum interval has not elapsed",
			threshold: dloadThreshold{
				load:             5,
				minTraceInterval: 10 * time.Second,
			},
			load:        6,
			lastTraceAt: sampledAt.Add(-9 * time.Second),
		},
		{
			name: "debug bypasses threshold",
			threshold: dloadThreshold{
				load:    5,
				isDebug: true,
			},
			load:     0,
			expected: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tracer := &dloadTracing{threshold: test.threshold}
			container := &containerDloadInfo{
				dLoad:       [2]float64{test.load},
				lastTraceAt: test.lastTraceAt,
			}

			if actual := tracer.shouldTrace(container, sampledAt); actual != test.expected {
				t.Fatalf("shouldTrace() = %t, want %t", actual, test.expected)
			}
		})
	}
}

func TestUpdateLoad(t *testing.T) {
	info := &containerDloadInfo{}

	updateLoad(info, 1, 0)

	expectedRunnableAvg := [2]uint64{164, 34}
	if info.runnableAvg != expectedRunnableAvg {
		t.Fatalf("runnableAvg = %v, want %v", info.runnableAvg, expectedRunnableAvg)
	}
	expectedLoadAvg := [2]float64{0.08, 0.02}
	if info.loadAvg != expectedLoadAvg {
		t.Fatalf("loadAvg = %v, want %v", info.loadAvg, expectedLoadAvg)
	}
}
