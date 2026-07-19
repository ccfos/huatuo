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

package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"

	"huatuo-bamai/pkg/profiling"
)

func TestCLIProfileTypeAndRemovedFlags(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantError string
	}{
		{
			name: "memory type",
			args: []string{
				"--type", "memory",
				"--language", "c",
				"--memory-mode", "physical_alloc",
				"--pid", strconv.Itoa(os.Getpid()),
			},
		},
		{
			name: "maximum profiler processes",
			args: []string{
				"--type", "cpu",
				"--language", "c",
				"--pid", strconv.Itoa(os.Getpid()),
				"--max-concurrent-procs", "2",
			},
		},
		{
			name: "tracer ID",
			args: []string{
				"--type", "cpu",
				"--language", "c",
				"--pid", strconv.Itoa(os.Getpid()),
				"--tracer-id", "trace-123",
			},
		},
		{
			name:      "legacy mem type",
			args:      []string{"--type", "mem", "--language", "c"},
			wantError: `unsupported profiling type "mem" (expected: cpu or memory)`,
		},
		{
			name:      "removed flags option",
			args:      []string{"--type", "cpu", "--language", "c", "--flags", "ignored"},
			wantError: "flag provided but not defined: -flags",
		},
		{
			name:      "removed tool limit option",
			args:      []string{"--type", "cpu", "--language", "c", "--tool-limit", "2"},
			wantError: "flag provided but not defined: -tool-limit",
		},
		{
			name:      "removed metadata option",
			args:      []string{"--type", "cpu", "--language", "c", "--metadata", "tracer_id=trace-123"},
			wantError: "flag provided but not defined: -metadata",
		},
		{
			name:      "removed CPU idle metadata option",
			args:      []string{"--type", "cpu", "--language", "c", "--cpuidle-metadata", "user=1"},
			wantError: "flag provided but not defined: -cpuidle-metadata",
		},
		{
			name:      "removed CPU sys metadata option",
			args:      []string{"--type", "cpu", "--language", "c", "--cpusys-metadata", "sys=1"},
			wantError: "flag provided but not defined: -cpusys-metadata",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := &cli.App{
				Flags:         appFlags,
				Before:        runBefore,
				AllowExtFlags: true,
				Writer:        io.Discard,
				ErrWriter:     io.Discard,
				Action:        func(*cli.Context) error { return nil },
			}
			err := app.Run(append([]string{"profiler"}, tt.args...))
			if tt.wantError == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantError, fmt.Sprintf("args: %v", tt.args))
		})
	}
}

func TestParseCPUIDsWithLimit(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		numCPU  int
		want    []int
		wantErr bool
	}{
		{
			numCPU: 8,
			name:   "single CPU",
			input:  "1",
			want:   []int{1},
		},
		{
			numCPU: 8,
			name:   "comma separated",
			input:  "1,3,5",
			want:   []int{1, 3, 5},
		},
		{
			numCPU: 8,
			name:   "range",
			input:  "1-3",
			want:   []int{1, 2, 3},
		},
		{
			numCPU: 8,
			name:   "mixed",
			input:  "1,3,5-7",
			want:   []int{1, 3, 5, 6, 7},
		},
		{
			numCPU: 8,
			name:   "with spaces",
			input:  "1, 3, 5-7",
			want:   []int{1, 3, 5, 6, 7},
		},
		{
			numCPU: 8,
			name:   "duplicate removal",
			input:  "1,1,2-3,3",
			want:   []int{1, 2, 3},
		},
		{
			numCPU: 8,
			name:   "range with spaces",
			input:  "1 - 3",
			want:   []int{1, 2, 3},
		},
		{
			numCPU:  8,
			name:    "invalid range",
			input:   "3-1",
			wantErr: true,
		},
		{
			numCPU:  8,
			name:    "out of range",
			input:   "8",
			wantErr: true,
		},
		{
			numCPU:  8,
			name:    "negative",
			input:   "-1",
			wantErr: true,
		},
		{
			numCPU:  8,
			name:    "invalid format",
			input:   "a,b",
			wantErr: true,
		},
		{
			numCPU:  8,
			name:    "empty after trim",
			input:   "  ",
			wantErr: true,
		},
		{
			numCPU: 8,
			name:   "valid max CPU",
			input:  "7",
			want:   []int{7},
		},
		{
			numCPU: 8,
			name:   "valid full range",
			input:  "0-7",
			want:   []int{0, 1, 2, 3, 4, 5, 6, 7},
		},
		{
			numCPU:  8,
			name:    "range end out of range",
			input:   "0-8",
			wantErr: true,
		},
		{
			name:    "invalid cpu count",
			input:   "0",
			numCPU:  0,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCPUIDsWithLimit(tt.input, tt.numCPU)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidateProfilerFlagCompatibility(t *testing.T) {
	tests := []struct {
		name      string
		language  string
		typ       string
		args      []string
		wantError string
	}{
		{name: "native CPU cpuid", language: "go", typ: "cpu", args: []string{"--cpuid", "1"}},
		{name: "native off-CPU", language: "go", typ: "cpu", args: []string{"--cpu-mode", "offcpu"}},
		{
			name:      "Java off-CPU",
			language:  "java",
			typ:       "cpu",
			args:      []string{"--cpu-mode", "offcpu"},
			wantError: "--cpu-mode=offcpu is supported only by native CPU profiling",
		},
		{
			name:      "off-CPU rejects cpuid",
			language:  "c",
			typ:       "cpu",
			args:      []string{"--cpu-mode", "offcpu", "--cpuid", "1"},
			wantError: "--cpuid is not supported with --cpu-mode=offcpu",
		},
		{
			name:      "off-CPU rejects explicit frequency",
			language:  "go",
			typ:       "cpu",
			args:      []string{"--cpu-mode", "offcpu", "--freq", "99"},
			wantError: "--freq is not used with --cpu-mode=offcpu",
		},
		{
			name:      "off-CPU duration range",
			language:  "c++",
			typ:       "cpu",
			args:      []string{"--cpu-mode", "offcpu", "--offcpu-min-us", "100", "--offcpu-max-us", "99"},
			wantError: "--offcpu-max-us must be zero or at least --offcpu-min-us",
		},
		{
			name:      "off-CPU metric requires mode",
			language:  "go",
			typ:       "cpu",
			args:      []string{"--offcpu-metric", "blocked"},
			wantError: "--offcpu-metric requires native CPU profiling with --cpu-mode=offcpu",
		},
		{
			name:      "invalid off-CPU metric",
			language:  "go",
			typ:       "cpu",
			args:      []string{"--cpu-mode", "offcpu", "--offcpu-metric", "wait"},
			wantError: `unsupported off-CPU metric "wait" (expected: total, blocked, or runnable)`,
		},
		{
			name:      "Java cpuid",
			language:  "java",
			typ:       "cpu",
			args:      []string{"--cpuid", "1"},
			wantError: "--cpuid is supported only by native CPU profiling",
		},
		{
			name:      "Python BPF debug",
			language:  "python",
			typ:       "cpu",
			args:      []string{"--log-bpf-debug"},
			wantError: "--log-bpf-debug is supported only by native profilers",
		},
		{
			name:      "Java thread group",
			language:  "java",
			typ:       "cpu",
			args:      []string{"--thread-group"},
			wantError: "--thread-group is supported only by native profiling",
		},
		{
			name:      "Python thread group",
			language:  "python",
			typ:       "cpu",
			args:      []string{"--thread-group"},
			wantError: "--thread-group is supported only by native profiling",
		},
		{name: "native CPU thread group", language: "go", typ: "cpu", args: []string{"--thread-group"}},
		{name: "native memory thread group", language: "c", typ: "memory", args: []string{"--thread-group"}},
		{
			name:      "native exec path",
			language:  "c",
			typ:       "cpu",
			args:      []string{"--binary-match-path", "/bin/app"},
			wantError: "--binary-match-path is not supported by native profilers",
		},
		{
			name:      "Python physical memory probability",
			language:  "python",
			typ:       "cpu",
			args:      []string{"--physical-memory-probability", "10"},
			wantError: "--physical-memory-probability is supported only by native physical memory profiling",
		},
		{
			name:      "native virtual memory probability",
			language:  "c",
			typ:       "memory",
			args:      []string{"--memory-mode", "virtual_alloc", "--physical-memory-probability", "10"},
			wantError: "--physical-memory-probability is supported only by native physical memory profiling",
		},
		{
			name:     "native physical memory probability",
			language: "c",
			typ:      "memory",
			args:     []string{"--memory-mode", "physical_usage", "--physical-memory-probability", "10"},
		},
		{
			name:      "native physical memory probability out of range",
			language:  "c",
			typ:       "memory",
			args:      []string{"--memory-mode", "physical_alloc", "--physical-memory-probability", "101"},
			wantError: "physical memory probability must be between 1 and 100",
		},
		{
			name:      "Java frequency too high",
			language:  "java",
			typ:       "cpu",
			args:      []string{"--freq", "1001"},
			wantError: "Java profiler frequency must not exceed 1000 samples per second",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newValidationCLIContext(t, tt.args...)
			err := validateProfilerFlagCompatibility(
				ctx,
				profiling.Language(tt.language),
				profiling.Type(tt.typ),
			)
			if tt.wantError != "" {
				require.EqualError(t, err, tt.wantError)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateOutputFormat(t *testing.T) {
	for _, format := range []string{"collapsed", "flamegraph", "svg", "remote"} {
		require.NoError(t, validateOutputFormat(format))
	}
	require.EqualError(t, validateOutputFormat("pprof"), `unsupported output format "pprof"`)
}

func TestValidateNumericOptions(t *testing.T) {
	require.NoError(t, validateNumericOptions("cpu", 99, 0))
	require.NoError(t, validateNumericOptions(profiling.TypeMemory, 0, 0))
	require.EqualError(t, validateNumericOptions("cpu", 0, 0), "frequency must be at least 1 sample per second")
	require.EqualError(
		t,
		validateNumericOptions("cpu", 99, -1),
		"maximum profiler processes must not be negative",
	)
}

func newValidationCLIContext(t *testing.T, args ...string) *cli.Context {
	t.Helper()
	set := flag.NewFlagSet(t.Name(), flag.ContinueOnError)
	for _, appFlag := range appFlags {
		require.NoError(t, appFlag.Apply(set))
	}
	require.NoError(t, set.Parse(args))
	return cli.NewContext(nil, set, nil)
}

func TestParseCPUIDs(t *testing.T) {
	numCPU := runtime.NumCPU()

	t.Run("out of range based on numCPU", func(t *testing.T) {
		_, err := parseCPUIDs(strconv.Itoa(numCPU))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "out of range")
	})

	t.Run("valid max CPU", func(t *testing.T) {
		if numCPU > 0 {
			got, err := parseCPUIDs("0")
			require.NoError(t, err)
			assert.Equal(t, []int{0}, got)
		}
	})
}

func TestValidateMemoryMode(t *testing.T) {
	tests := []struct {
		name      string
		language  string
		typ       string
		mode      string
		wantError string
	}{
		{
			name:     "Java object allocation",
			language: "java",
			typ:      "memory",
			mode:     "object_alloc",
		},
		{
			name:     "Java object usage",
			language: "java",
			typ:      "memory",
			mode:     "object_usage",
		},
		{
			name:     "native physical allocation",
			language: "c",
			typ:      "memory",
			mode:     "physical_alloc",
		},
		{
			name:      "memory mode required",
			language:  "java",
			typ:       "memory",
			wantError: "--memory-mode is required when --type=memory",
		},
		{
			name:      "memory mode rejected for CPU",
			language:  "java",
			typ:       "cpu",
			mode:      "object_alloc",
			wantError: "--memory-mode is only valid when --type=memory",
		},
		{
			name:      "Java rejects native mode",
			language:  "java",
			typ:       "memory",
			mode:      "physical_alloc",
			wantError: "memory mode \"physical_alloc\" is not supported for java; supported modes: object_alloc, object_usage",
		},
		{
			name:      "native rejects object mode",
			language:  "go",
			typ:       "memory",
			mode:      "object_alloc",
			wantError: "memory mode \"object_alloc\" is not supported for go; supported modes: virtual_alloc, physical_alloc, physical_usage",
		},
		{
			name:     "Python is validated before memory mode",
			language: "python",
			typ:      "cpu",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMemoryMode(
				profiling.Language(tt.language),
				profiling.Type(tt.typ),
				tt.mode,
			)
			if tt.wantError != "" {
				require.EqualError(t, err, tt.wantError)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateAggregationWindow(t *testing.T) {
	tests := []struct {
		name      string
		duration  int
		interval  int
		wantError string
	}{
		{name: "equal", duration: 10, interval: 10},
		{name: "shorter interval", duration: 10, interval: 3},
		{name: "invalid duration", interval: 1, wantError: "duration must be at least 1 second"},
		{name: "invalid interval", duration: 10, wantError: "aggregation interval must be at least 1 second"},
		{
			name:      "interval exceeds duration",
			duration:  10,
			interval:  11,
			wantError: "aggregation interval (11s) exceeds duration (10s)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAggregationWindow(tt.duration, tt.interval)
			if tt.wantError != "" {
				require.EqualError(t, err, tt.wantError)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidatePythonProfileOptions(t *testing.T) {
	tests := []struct {
		name      string
		language  string
		typ       string
		duration  int
		interval  int
		wantError string
	}{
		{name: "Python CPU one-shot", language: "python", typ: "cpu", duration: 10, interval: 10},
		{
			name:      "Python memory",
			language:  "python",
			typ:       "memory",
			duration:  10,
			interval:  10,
			wantError: "Python profiler supports only --type=cpu",
		},
		{
			name:      "Python continuous profiling",
			language:  "python",
			typ:       "cpu",
			duration:  30,
			interval:  10,
			wantError: "Python CPU profiler does not support continuous profiling: --aggr-interval (10s) must equal --duration (30s)",
		},
		{name: "Java unaffected", language: "java", typ: "cpu", duration: 30, interval: 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePythonProfileOptions(
				profiling.Language(tt.language),
				profiling.Type(tt.typ),
				tt.duration,
				tt.interval,
			)
			if tt.wantError != "" {
				require.EqualError(t, err, tt.wantError)
				return
			}
			require.NoError(t, err)
		})
	}
}
