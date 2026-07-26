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
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type failingCPUStatReader struct {
	err error
}

func (r failingCPUStatReader) Read([]byte) (int, error) {
	return 0, r.err
}

func TestNewCPUSysBindsConfig(t *testing.T) {
	originalConfig := cfg
	t.Cleanup(func() {
		Set(originalConfig)
	})

	testConfig := &Config{}
	testConfig.CPUSys.Interval = 12
	testConfig.CPUSys.RunTracingToolTimeout = 7
	testConfig.CPUSys.SysThreshold = 50
	testConfig.CPUSys.DeltaSysThreshold = 15
	Set(testConfig)

	attr, err := newCPUSys()
	if err != nil {
		t.Fatalf("newCPUSys() error = %v", err)
	}
	tracer, ok := attr.TracingData.(*cpuSysTracing)
	if !ok {
		t.Fatalf("TracingData type = %T, want *cpuSysTracing", attr.TracingData)
	}
	if tracer.interval != 12*time.Second {
		t.Errorf("interval = %s, want 12s", tracer.interval)
	}
	if tracer.perfDuration != 7*time.Second {
		t.Errorf("perfDuration = %s, want 7s", tracer.perfDuration)
	}
	if tracer.threshold != (cpuSysThreshold{usage: 50, delta: 15}) {
		t.Errorf("threshold = %+v, want usage=50 delta=15", tracer.threshold)
	}
}

func TestParseCPUUsage(t *testing.T) {
	t.Parallel()

	readErr := errors.New("read failed")
	tests := []struct {
		name      string
		input     string
		reader    io.Reader
		expected  cpuUsage
		wantError string
	}{
		{
			name:     "all counters",
			input:    "cpu 100 10 30 860 5 2 3 1 50 4\n",
			expected: cpuUsage{system: 30, total: 1011},
		},
		{
			name:     "minimum counters",
			input:    "cpu 1 2 3 4\n",
			expected: cpuUsage{system: 3, total: 10},
		},
		{
			name:      "empty input",
			wantError: "cpu statistics are empty",
		},
		{
			name:      "reader failure",
			reader:    failingCPUStatReader{err: readErr},
			wantError: "scan cpu statistics: read failed",
		},
		{
			name:      "unexpected label",
			input:     "cpu0 1 2 3 4\n",
			wantError: `unexpected cpu statistics label "cpu0"`,
		},
		{
			name:      "too few counters",
			input:     "cpu 1 2 3\n",
			wantError: "cpu statistics require at least 4 counters",
		},
		{
			name:      "invalid counter",
			input:     "cpu 1 2 invalid 4\n",
			wantError: `parse cpu system counter "invalid"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reader := tt.reader
			if reader == nil {
				reader = strings.NewReader(tt.input)
			}
			actual, err := parseCPUUsage(reader)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("parseCPUUsage() error = %v, want contain %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCPUUsage() error = %v", err)
			}
			if actual != tt.expected {
				t.Fatalf("parseCPUUsage() = %+v, want %+v", actual, tt.expected)
			}
		})
	}
}

func TestCPUSysStateUpdate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		samples          []cpuUsage
		expectedValid    []bool
		expectedPercent  int64
		expectedDelta    int64
		hasSystemPercent bool
	}{
		{
			name: "consecutive samples",
			samples: []cpuUsage{
				{system: 20, total: 100},
				{system: 40, total: 200},
				{system: 70, total: 300},
			},
			expectedValid:    []bool{false, true, true},
			expectedPercent:  30,
			expectedDelta:    10,
			hasSystemPercent: true,
		},
		{
			name: "unchanged counters",
			samples: []cpuUsage{
				{system: 20, total: 100},
				{system: 20, total: 100},
			},
			expectedValid: []bool{false, false},
		},
		{
			name: "counter reset",
			samples: []cpuUsage{
				{system: 100, total: 1000},
				{system: 120, total: 1100},
				{system: 10, total: 100},
				{system: 30, total: 200},
			},
			expectedValid:    []bool{false, true, false, true},
			expectedPercent:  20,
			hasSystemPercent: true,
		},
		{
			name: "system delta exceeds total delta",
			samples: []cpuUsage{
				{system: 10, total: 100},
				{system: 20, total: 200},
				{system: 100, total: 250},
			},
			expectedValid: []bool{false, true, false},
		},
		{
			name: "system advances without total",
			samples: []cpuUsage{
				{system: 10, total: 100},
				{system: 20, total: 200},
				{system: 30, total: 200},
			},
			expectedValid: []bool{false, true, false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			state := &cpuSysState{}
			for i, sample := range tt.samples {
				if actual := state.update(sample); actual != tt.expectedValid[i] {
					t.Fatalf(
						"update(sample %d) = %t, want %t",
						i,
						actual,
						tt.expectedValid[i],
					)
				}
			}
			if state.systemPercent != tt.expectedPercent {
				t.Errorf(
					"systemPercent = %d, want %d",
					state.systemPercent,
					tt.expectedPercent,
				)
			}
			if state.systemPercentDelta != tt.expectedDelta {
				t.Errorf(
					"systemPercentDelta = %d, want %d",
					state.systemPercentDelta,
					tt.expectedDelta,
				)
			}
			if state.hasSystemPercent != tt.hasSystemPercent {
				t.Errorf(
					"hasSystemPercent = %t, want %t",
					state.hasSystemPercent,
					tt.hasSystemPercent,
				)
			}
		})
	}
}

func TestCPUSysStateShouldTrace(t *testing.T) {
	t.Parallel()

	threshold := cpuSysThreshold{usage: 45, delta: 20}
	tests := []struct {
		name          string
		systemPercent int64
		systemDelta   int64
		expected      bool
	}{
		{name: "both exceed thresholds", systemPercent: 52, systemDelta: 25, expected: true},
		{name: "only usage exceeds", systemPercent: 52, systemDelta: 10},
		{name: "only delta exceeds", systemPercent: 40, systemDelta: 25},
		{name: "values equal thresholds", systemPercent: 45, systemDelta: 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			state := cpuSysState{
				systemPercent:      tt.systemPercent,
				systemPercentDelta: tt.systemDelta,
			}
			if actual := state.shouldTrace(threshold); actual != tt.expected {
				t.Fatalf("shouldTrace() = %t, want %t", actual, tt.expected)
			}
		})
	}
}

func TestValidateCPUSysConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		intervalSeconds      int64
		perfDurationSeconds  int64
		systemThreshold      int64
		systemDeltaThreshold int64
		wantError            string
	}{
		{
			name:                 "valid",
			intervalSeconds:      10,
			perfDurationSeconds:  10,
			systemThreshold:      45,
			systemDeltaThreshold: 20,
		},
		{
			name:                 "zero interval",
			perfDurationSeconds:  10,
			systemThreshold:      45,
			systemDeltaThreshold: 20,
			wantError:            "cpu system interval must be positive",
		},
		{
			name:                 "zero perf duration",
			intervalSeconds:      10,
			systemThreshold:      45,
			systemDeltaThreshold: 20,
			wantError:            "cpu system perf duration must be positive",
		},
		{
			name:                 "interval duration overflow",
			intervalSeconds:      maxIntervalSeconds + 1,
			perfDurationSeconds:  10,
			systemThreshold:      45,
			systemDeltaThreshold: 20,
			wantError:            "cpu system interval must not exceed",
		},
		{
			name:                 "perf duration overflow",
			intervalSeconds:      10,
			perfDurationSeconds:  maxPerfDurationSeconds + 1,
			systemThreshold:      45,
			systemDeltaThreshold: 20,
			wantError:            "cpu system perf duration must not exceed",
		},
		{
			name:                 "negative system threshold",
			intervalSeconds:      10,
			perfDurationSeconds:  10,
			systemThreshold:      -1,
			systemDeltaThreshold: 20,
			wantError:            "cpu system threshold must be between 0 and 100",
		},
		{
			name:                 "system threshold above maximum",
			intervalSeconds:      10,
			perfDurationSeconds:  10,
			systemThreshold:      101,
			systemDeltaThreshold: 20,
			wantError:            "cpu system threshold must be between 0 and 100",
		},
		{
			name:                 "negative delta threshold",
			intervalSeconds:      10,
			perfDurationSeconds:  10,
			systemThreshold:      45,
			systemDeltaThreshold: -1,
			wantError:            "cpu system delta threshold must be between 0 and 100",
		},
		{
			name:                 "delta threshold above maximum",
			intervalSeconds:      10,
			perfDurationSeconds:  10,
			systemThreshold:      45,
			systemDeltaThreshold: 101,
			wantError:            "cpu system delta threshold must be between 0 and 100",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateCPUSysConfig(
				tt.intervalSeconds,
				tt.perfDurationSeconds,
				tt.systemThreshold,
				tt.systemDeltaThreshold,
			)
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("validateCPUSysConfig() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("validateCPUSysConfig() error = %v, want contain %q", err, tt.wantError)
			}
		})
	}
}

func BenchmarkParseCPUUsage(b *testing.B) {
	const input = "cpu 100 10 30 860 5 2 3 1 50 4\n"

	b.ReportAllocs()
	for b.Loop() {
		if _, err := parseCPUUsage(strings.NewReader(input)); err != nil {
			b.Fatal(err)
		}
	}
}
