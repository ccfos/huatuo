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

package main

import (
	"flag"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/ccfos/huatuo/pkg/profiling"
)

func TestParsePIDs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     string
		want      []int
		wantError bool
	}{
		{name: "empty"},
		{name: "single PID", value: "123", want: []int{123}},
		{name: "multiple PIDs", value: "123,456", want: []int{123, 456}},
		{name: "spaces", value: "123, 456", want: []int{123, 456}},
		{name: "empty item", value: "123,,456", wantError: true},
		{name: "non-numeric", value: "123,abc", wantError: true},
		{name: "zero", value: "0", wantError: true},
		{name: "negative", value: "-1", wantError: true},
		{name: "duplicate", value: "123,123", wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parsePIDs(tt.value)
			if tt.wantError {
				if err == nil {
					t.Fatalf("parsePIDs(%q) error=nil, want error", tt.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePIDs(%q) error=%v", tt.value, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("parsePIDs(%q)=%v, want %v", tt.value, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("parsePIDs(%q)=%v, want %v", tt.value, got, tt.want)
				}
			}
		})
	}
}

func TestLoadConfig(t *testing.T) {
	set := flag.NewFlagSet(t.Name(), flag.ContinueOnError)
	set.String("type", "cpu", "")
	set.String("language", "c", "")
	set.String("output-format", "collapsed", "")
	set.String("task-id", "trace-123", "")
	set.String("tool-path", "/opt/huatuo/tools", "")
	set.String("pid", "123,456", "")
	set.String("cpuid", "0", "")
	set.Int("duration", 30, "")
	set.Int("aggr-interval", 10, "")
	set.Bool("offcpu-stats", true, "")
	set.Bool("require-hardware-pmu", true, "")
	cfg, err := loadConfig(cli.NewContext(nil, set, nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider.Duration != 30*time.Second || cfg.Provider.AggregationInterval != 10*time.Second {
		t.Fatalf("incorrect time units: %+v", cfg.Provider)
	}
	if cfg.Provider.ToolDir != "/opt/huatuo/tools" || cfg.TaskID != "trace-123" || !cfg.Provider.OffCPUStatsEnabled || !cfg.Provider.RequireHardwarePMU {
		t.Fatalf("lost CLI settings: %+v", cfg)
	}
	if cfg.Provider.Mode != profiling.ModeOnCPU || cfg.Provider.OffCPUPhase != profiling.OffCPUPhaseAll {
		t.Fatalf("lost defaults: %+v", cfg.Provider)
	}
	if cfg.Provider.PID() != 123 || len(cfg.Provider.PIDs) != 2 || len(cfg.Provider.CPUIDs) != 1 || cfg.Provider.CPUIDs[0] != 0 {
		t.Fatalf("incorrect target selection: %+v", cfg.Provider)
	}
}

func TestParseSeconds(t *testing.T) {
	const maxSeconds = int64(math.MaxInt64) / int64(time.Second)
	for _, tc := range []struct {
		name      string
		seconds   int64
		wantError bool
	}{
		{name: "one second", seconds: 1},
		{name: "maximum", seconds: maxSeconds},
		{name: "zero", wantError: true},
		{name: "negative", seconds: -1, wantError: true},
		{name: "overflow", seconds: maxSeconds + 1, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if strconv.IntSize == 32 && tc.seconds > math.MaxInt32 {
				t.Skip("seconds exceed int on this architecture")
			}
			got, err := parseSeconds("duration", int(tc.seconds))
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v, want error=%v", err, tc.wantError)
			}
			if err == nil && got/time.Second != time.Duration(tc.seconds) {
				t.Fatalf("duration=%s, want %d seconds", got, tc.seconds)
			}
		})
	}
}

func TestLoadPythonMemoryConfig(t *testing.T) {
	app := &cli.App{Flags: appFlags, Action: func(ctx *cli.Context) error {
		cfg, err := loadConfig(ctx)
		if err != nil {
			return err
		}
		if cfg.Provider.Mode != profiling.ModeObjectUsage || cfg.Provider.PythonMemoryStackMode != "hybrid" || !cfg.Provider.PythonMemoryMergeThreads {
			t.Fatalf("lost Python memory configuration: %+v", cfg.Provider)
		}
		return nil
	}}
	if err := app.Run([]string{"profiler", "--type", "memory", "--language", "python", "--python-memory-stack", "hybrid", "--python-memory-merge-threads"}); err != nil {
		t.Fatal(err)
	}
}
