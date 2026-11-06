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
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"

	"github.com/ccfos/huatuo/pkg/profiling"
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
			name: "task ID",
			args: []string{
				"--type", "cpu",
				"--language", "c",
				"--pid", strconv.Itoa(os.Getpid()),
				"--task-id", "trace-123",
			},
		},
		{
			name: "remote output requires task ID",
			args: []string{
				"--type", "cpu",
				"--language", "c",
				"--pid", strconv.Itoa(os.Getpid()),
				"--output-format", "remote-store",
				"--output-path", "/var/run/huatuo-toolstream.sock",
			},
			wantError: "--task-id must not be empty when --output-format=remote-store",
		},
		{
			name:      "legacy mem type",
			args:      []string{"--type", "mem", "--language", "c"},
			wantError: `unsupported profiling type "mem"`,
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
			err := app.Run(append([]string{"profiler", "--output-path", t.TempDir(), "--output-format", "collapsed"}, tt.args...))
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
		{name: "native hardware PMU", language: "go", typ: "cpu", args: []string{"--require-hardware-pmu"}},
		{name: "native off-CPU", language: "go", typ: "cpu", args: []string{"--cpu-mode", "offcpu"}},
		{
			name:      "Java off-CPU",
			language:  "java",
			typ:       "cpu",
			args:      []string{"--cpu-mode", "offcpu"},
			wantError: "--cpu-mode=offcpu is supported only by native CPU profiling",
		},
		{
			name:     "native off-CPU cpuid",
			language: "c",
			typ:      "cpu",
			args:     []string{"--cpu-mode", "offcpu", "--cpuid", "1"},
		},
		{
			name:     "native off-CPU stats",
			language: "c",
			typ:      "cpu",
			args:     []string{"--cpu-mode", "offcpu", "--offcpu-stats"},
		},
		{
			name:      "off-CPU stats require mode",
			language:  "go",
			typ:       "cpu",
			args:      []string{"--offcpu-stats"},
			wantError: "--offcpu-stats requires native CPU profiling with --cpu-mode=offcpu",
		},
		{
			name:      "off-CPU rejects explicit frequency",
			language:  "go",
			typ:       "cpu",
			args:      []string{"--cpu-mode", "offcpu", "--freq", "99"},
			wantError: "--freq is not used with --cpu-mode=offcpu",
		},
		{
			name:      "off-CPU rejects hardware PMU",
			language:  "go",
			typ:       "cpu",
			args:      []string{"--cpu-mode", "offcpu", "--require-hardware-pmu"},
			wantError: "--require-hardware-pmu requires native CPU profiling with --cpu-mode=oncpu",
		},
		{
			name:      "off-CPU minimum duration requires mode",
			language:  "c++",
			typ:       "cpu",
			args:      []string{"--offcpu-min-duration-us", "100"},
			wantError: "--offcpu-min-duration-us requires native CPU profiling with --cpu-mode=offcpu",
		},
		{
			name:      "off-CPU metric requires mode",
			language:  "go",
			typ:       "cpu",
			args:      []string{"--offcpu-phase", "blocked"},
			wantError: "--offcpu-phase requires native CPU profiling with --cpu-mode=offcpu",
		},
		{
			name:      "invalid off-CPU phase",
			language:  "go",
			typ:       "cpu",
			args:      []string{"--cpu-mode", "offcpu", "--offcpu-phase", "wait"},
			wantError: `unsupported off-CPU phase "wait"`,
		},
		{
			name:      "Java cpuid",
			language:  "java",
			typ:       "cpu",
			args:      []string{"--cpuid", "1"},
			wantError: "--cpuid is supported only by native CPU profiling",
		},
		{
			name:      "Java hardware PMU",
			language:  "java",
			typ:       "cpu",
			args:      []string{"--require-hardware-pmu"},
			wantError: "--require-hardware-pmu requires native CPU profiling with --cpu-mode=oncpu",
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

func TestOffCPUStatsFlag(t *testing.T) {
	require.False(t, newValidationCLIContext(t).Bool("offcpu-stats"))
	require.True(t, newValidationCLIContext(t, "--offcpu-stats").Bool("offcpu-stats"))
}

func TestValidateOutputFormat(t *testing.T) {
	for _, format := range []string{"collapsed", "flamegraph", "svg", "json", "remote-store"} {
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

func TestCLITargetFilters(t *testing.T) {
	tests := []struct {
		name        string
		language    string
		profileType string
		modeFlag    string
		mode        string
		wantError   bool
	}{
		{
			name:        "C on-CPU intersection",
			language:    "c",
			profileType: "cpu",
			modeFlag:    "--cpu-mode",
			mode:        "oncpu",
		},
		{
			name:        "C off-CPU intersection",
			language:    "c",
			profileType: "cpu",
			modeFlag:    "--cpu-mode",
			mode:        "offcpu",
		},
		{
			name:        "Go on-CPU intersection",
			language:    "go",
			profileType: "cpu",
			modeFlag:    "--cpu-mode",
			mode:        "oncpu",
		},
		{
			name:        "Go off-CPU intersection",
			language:    "go",
			profileType: "cpu",
			modeFlag:    "--cpu-mode",
			mode:        "offcpu",
		},
		{
			name:        "C++ on-CPU intersection",
			language:    "c++",
			profileType: "cpu",
			modeFlag:    "--cpu-mode",
			mode:        "oncpu",
		},
		{
			name:        "C++ off-CPU intersection",
			language:    "c++",
			profileType: "cpu",
			modeFlag:    "--cpu-mode",
			mode:        "offcpu",
		},
		{
			name:        "C memory conflict",
			language:    "c",
			profileType: "memory",
			modeFlag:    "--memory-mode",
			mode:        "virtual_alloc",
			wantError:   true,
		},
		{
			name:        "Go memory conflict",
			language:    "go",
			profileType: "memory",
			modeFlag:    "--memory-mode",
			mode:        "virtual_alloc",
			wantError:   true,
		},
		{
			name:        "C++ memory conflict",
			language:    "c++",
			profileType: "memory",
			modeFlag:    "--memory-mode",
			mode:        "virtual_alloc",
			wantError:   true,
		},
		{
			name:        "Java CPU conflict before tool discovery",
			language:    "java",
			profileType: "cpu",
			wantError:   true,
		},
		{
			name:        "Java memory conflict before tool discovery",
			language:    "java",
			profileType: "memory",
			modeFlag:    "--memory-mode",
			mode:        "object_alloc",
			wantError:   true,
		},
		{
			name:        "Python CPU conflict before tool discovery",
			language:    "python",
			profileType: "cpu",
			wantError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actionCalled := false
			app := &cli.App{
				Flags:     appFlags,
				Before:    runBefore,
				Writer:    io.Discard,
				ErrWriter: io.Discard,
				Action: func(*cli.Context) error {
					actionCalled = true
					return nil
				},
			}
			args := []string{
				"profiler",
				"--language", tt.language,
				"--type", tt.profileType,
				"--pid", strconv.Itoa(os.Getpid()),
				"--container-id", strings.Repeat("a", 64),
				"--output-format", "collapsed",
				"--output-path", filepath.Join(t.TempDir(), "profile"),
			}
			if tt.modeFlag != "" {
				args = append(args, tt.modeFlag, tt.mode)
			}
			err := app.Run(args)
			if tt.wantError {
				require.EqualError(t, err, "--pid and --container-id are mutually exclusive")
				require.False(t, actionCalled, "target conflict reached collection")
				return
			}
			require.NoError(t, err)
			require.True(t, actionCalled, "native CPU combined target filters were rejected")
		})
	}
}

func TestValidateLanguageOptionsToolRoot(t *testing.T) {
	root := t.TempDir()
	for _, language := range []profiling.Language{
		profiling.LanguageJava, profiling.LanguagePython, profiling.LanguageGo,
	} {
		t.Run(string(language), func(t *testing.T) {
			for _, directory := range []string{"", root, filepath.Join(root, "missing")} {
				ctx := newValidationCLIContext(t, "--pid", "1", "--tool-path", directory)
				err := validateLanguageOptions(ctx, language, profiling.TypeCPU)
				wantError := language != profiling.LanguageGo && directory != root
				if (err != nil) != wantError {
					t.Errorf("validateLanguageOptions(%q) error = %v, want error %t", directory, err, wantError)
				}
			}
		})
	}
}

func TestCLIOutputOptions(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		wantError string
	}{
		{name: "both missing", wantError: "must both be specified"},
		{name: "format missing", args: []string{"--output-path", "profile"}, wantError: "must both be specified"},
		{name: "path missing", args: []string{"--output-format", "collapsed"}, wantError: "must both be specified"},
		{name: "empty path", args: []string{"--output-format", "collapsed", "--output-path", ""}, wantError: "must not be empty"},
		{name: "stdout", args: []string{"--output-format", "collapsed", "--output-path", "stdout"}},
		{name: "stdout alias", args: []string{"--output-format", "collapsed", "--output-path", "-"}},
		{name: "collapsed", args: []string{"--output-format", "collapsed", "--output-path", "profile"}},
		{name: "json", args: []string{"--output-format", "json", "--output-path", "profile.json"}},
		{name: "remote-store", args: []string{"--output-format", "remote-store", "--output-path", "/run/toolstream.sock", "--task-id", "task"}},
		{name: "removed storage flag", args: []string{"--output-storage", "/run/toolstream.sock"}, wantError: "flag provided but not defined"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := &cli.App{Flags: appFlags, Before: runBefore, Writer: io.Discard, ErrWriter: io.Discard, Action: func(*cli.Context) error { return nil }}
			args := []string{"profiler", "--type", "cpu", "--language", "c", "--pid", strconv.Itoa(os.Getpid())}
			err := app.Run(append(args, tc.args...))
			if tc.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.wantError)
			}
		})
	}
}

func TestValidateLanguageOptionsTargetSelection(t *testing.T) {
	const containerID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	toolDir := t.TempDir()

	tests := []struct {
		name    string
		lang    profiling.Language
		typ     profiling.Type
		args    []string
		wantErr string
	}{
		{
			name: "native cpu rejects both targets",
			lang: profiling.LanguageC, typ: profiling.TypeCPU,
			args:    []string{"--pid", "1", "--container-id", containerID},
			wantErr: "--container-id and --pid cannot be used together",
		},
		{
			name: "native cpu samples the host without a target",
			lang: profiling.LanguageC, typ: profiling.TypeCPU,
		},
		{
			name: "native cpu accepts a pid alone",
			lang: profiling.LanguageC, typ: profiling.TypeCPU,
			args: []string{"--pid", "1"},
		},
		{
			name: "native cpu accepts a container alone",
			lang: profiling.LanguageC, typ: profiling.TypeCPU,
			args: []string{"--container-id", containerID},
		},
		{
			name: "native memory rejects both targets",
			lang: profiling.LanguageGo, typ: profiling.TypeMemory,
			args:    []string{"--pid", "1", "--container-id", containerID},
			wantErr: "--container-id and --pid cannot be used together",
		},
		{
			name: "native memory needs a target",
			lang: profiling.LanguageGo, typ: profiling.TypeMemory,
			wantErr: "exactly one of --container-id or --pid must be provided",
		},
		{
			name: "java rejects both targets",
			lang: profiling.LanguageJava, typ: profiling.TypeCPU,
			args:    []string{"--pid", "1", "--container-id", containerID, "--tool-path", toolDir},
			wantErr: "--container-id and --pid cannot be used together",
		},
		{
			name: "python rejects both targets",
			lang: profiling.LanguagePython, typ: profiling.TypeCPU,
			args:    []string{"--pid", "1", "--container-id", containerID, "--tool-path", toolDir},
			wantErr: "--container-id and --pid cannot be used together",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newValidationCLIContext(t, tt.args...)
			err := validateLanguageOptions(ctx, tt.lang, tt.typ)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
