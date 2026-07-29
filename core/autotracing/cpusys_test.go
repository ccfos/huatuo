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
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"huatuo-bamai/internal/procfs"
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
	testConfig.CPUSys.IntervalTracing = 300
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
	if tracer.minTraceInterval != 300*time.Second {
		t.Errorf("minTraceInterval = %s, want 5m0s", tracer.minTraceInterval)
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

func TestReadCPUUsageUsesProcfsPrefix(t *testing.T) {
	originalPrefix := filepath.Dir(procfs.DefaultPath())
	t.Cleanup(func() {
		procfs.RootPrefix(originalPrefix)
	})

	root := t.TempDir()
	procfs.RootPrefix(root)
	procDir := filepath.Join(root, "proc")
	if err := os.MkdirAll(procDir, 0o755); err != nil {
		t.Fatalf("os.MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(procDir, "stat"),
		[]byte("cpu 100 10 30 860\n"),
		0o600,
	); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	actual, err := readCPUUsage()
	if err != nil {
		t.Fatalf("readCPUUsage() error = %v", err)
	}
	expected := cpuUsage{system: 30, total: 1000}
	if actual != expected {
		t.Fatalf("readCPUUsage() = %+v, want %+v", actual, expected)
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

func TestCPUSysTracingShouldTrace(t *testing.T) {
	t.Parallel()

	sampledAt := time.Unix(1000, 0)
	tests := []struct {
		name          string
		systemPercent int64
		systemDelta   int64
		lastTraceAt   time.Time
		expected      bool
	}{
		{name: "both exceed thresholds", systemPercent: 52, systemDelta: 25, expected: true},
		{name: "only usage exceeds", systemPercent: 52, systemDelta: 10},
		{name: "only delta exceeds", systemPercent: 40, systemDelta: 25},
		{name: "values equal thresholds", systemPercent: 45, systemDelta: 20},
		{
			name:          "cooldown active",
			systemPercent: 52,
			systemDelta:   25,
			lastTraceAt:   sampledAt.Add(-299 * time.Second),
		},
		{
			name:          "cooldown elapsed",
			systemPercent: 52,
			systemDelta:   25,
			lastTraceAt:   sampledAt.Add(-300 * time.Second),
			expected:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			state := cpuSysState{
				systemPercent:      tt.systemPercent,
				systemPercentDelta: tt.systemDelta,
			}
			tracer := cpuSysTracing{
				minTraceInterval: 300 * time.Second,
				threshold:        cpuSysThreshold{usage: 45, delta: 20},
				lastTraceAt:      tt.lastTraceAt,
			}
			if actual := tracer.shouldTrace(state, sampledAt); actual != tt.expected {
				t.Fatalf("shouldTrace() = %t, want %t", actual, tt.expected)
			}
		})
	}
}

func TestCPUSysTracingDataJSON(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(cpuSysTracingData{
		SystemPercent:               52,
		SystemPercentThreshold:      45,
		SystemPercentDelta:          25,
		SystemPercentDeltaThreshold: 20,
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var actual map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &actual); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	expected := map[string]string{
		"system_percent":                 "52",
		"system_percent_threshold":       "45",
		"system_percent_delta":           "25",
		"system_percent_delta_threshold": "20",
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

func BenchmarkParseCPUUsage(b *testing.B) {
	const input = "cpu 100 10 30 860 5 2 3 1 50 4\n"

	b.ReportAllocs()
	for b.Loop() {
		if _, err := parseCPUUsage(strings.NewReader(input)); err != nil {
			b.Fatal(err)
		}
	}
}
