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
	"io"
	"math"
	"strings"
	"testing"

	"github.com/urfave/cli/v2"

	bpfabi "github.com/ccfos/huatuo/internal/bpf/abi"
	profileroutput "github.com/ccfos/huatuo/internal/profiler/output"
)

func TestValidateFlags(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "both missing", wantErr: "must both be specified"},
		{name: "format missing", args: []string{"--output-path", "profile"}, wantErr: "must both be specified"},
		{name: "path missing", args: []string{"--output-format", "collapsed"}, wantErr: "must both be specified"},
		{name: "empty path", args: []string{"--output-format", "collapsed", "--output-path", ""}, wantErr: "must not be empty"},
		{name: "stdout", args: []string{"--output-format", "collapsed", "--output-path", "stdout"}},
		{name: "stdout alias", args: []string{"--output-format", "collapsed", "--output-path", "-"}},
		{name: "collapsed", args: []string{"--output-format", "collapsed", "--output-path", "profile"}},
		{name: "flamegraph", args: []string{"--output-format", "flamegraph", "--output-path", "profile.svg"}},
		{name: "svg", args: []string{"--output-format", "svg", "--output-path", "profile.svg"}},
		{name: "json", args: []string{"--output-format", "json", "--output-path", "profile"}},
		{name: "remote-store", args: []string{"--output-format", "remote-store", "--output-path", "/run/toolstream.sock", "--task-id", "task"}},
		{name: "remote missing task", args: []string{"--output-format", "remote-store", "--output-path", "/run/toolstream.sock"}, wantErr: "--task-id must not be empty"},
		{name: "local task", args: []string{"--output-format", "collapsed", "--output-path", "profile", "--task-id", "task"}},
		{name: "unexpected argument", args: []string{"extra"}, wantErr: "unexpected arguments"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateFlags(newOutputFlagContext(t, tc.args...))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestRemovedOutputFlags(t *testing.T) {
	for _, name := range []string{"output", "output-storage"} {
		t.Run(name, func(t *testing.T) {
			app := &cli.App{Flags: appFlags(), Writer: io.Discard, ErrWriter: io.Discard}
			err := app.Run([]string{"irqtracing", "--" + name, "value"})
			if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
				t.Fatalf("removed flag accepted: %v", err)
			}
		})
	}
}

func TestLoadConfig(t *testing.T) {
	ctx := newOutputFlagContext(
		t,
		"--bpf-path", "/tmp/irqtracing.o",
		"--target-cpu", "4",
		"--duration", "5",
		"--max-events-per-second-per-cpu", "1000",
		"--output-format", "svg",
	)

	cfg, err := loadConfig(ctx)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.bpfPath != "/tmp/irqtracing.o" || cfg.targetCPU != 4 ||
		cfg.duration != 5 || cfg.maxEventsPerSecondPerCPU != 1000 ||
		cfg.output != profileroutput.FormatSVG {
		t.Fatalf("loadConfig() = %+v", cfg)
	}
}

func TestDefaultOutputIsCollapsed(t *testing.T) {
	cfg, err := loadConfig(newOutputFlagContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.output != profileroutput.FormatCollapsed {
		t.Fatalf("default output = %q, want collapsed", cfg.output)
	}
}

func TestValidateTargetCPU(t *testing.T) {
	tests := []struct {
		name    string
		cpu     int64
		wantErr bool
	}{
		{name: "all CPUs", cpu: -1},
		{name: "CPU zero", cpu: 0},
		{name: "largest supported CPU", cpu: 1<<31 - 1},
		{name: "invalid negative CPU", cpu: -2, wantErr: true},
		{name: "CPU exceeds BPF value", cpu: 1 << 31, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateTargetCPU(test.cpu)
			if test.wantErr && err == nil {
				t.Fatal("validateTargetCPU() error = nil")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("validateTargetCPU() error = %v", err)
			}
		})
	}
}

func TestAllCPUsTargetMatchesBPFABI(t *testing.T) {
	generated := bpfabi.IrqtracingTargetAllCpus
	target := allCPUsTarget
	if got, want := uint32(generated), uint32(target); got != want {
		t.Fatalf("generated all-CPU target bits = %#x, want %#x", got, want)
	}
}

func TestValidateDuration(t *testing.T) {
	maxInt := int64(math.MaxInt)
	largestSupportedDuration := maxDurationSeconds
	if maxInt < largestSupportedDuration {
		largestSupportedDuration = maxInt
	}

	tests := []struct {
		name     string
		duration int
		wantErr  bool
	}{
		{name: "one second", duration: 1},
		{name: "largest supported duration", duration: int(largestSupportedDuration)},
		{name: "zero", duration: 0, wantErr: true},
		{name: "negative", duration: -1, wantErr: true},
		{
			name:     "architecture maximum",
			duration: math.MaxInt,
			wantErr:  maxInt > maxDurationSeconds,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateDuration(test.duration)
			if test.wantErr && err == nil {
				t.Fatal("validateDuration() error = nil")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("validateDuration() error = %v", err)
			}
		})
	}
}

func TestValidateMaxEventsPerSecondPerCPU(t *testing.T) {
	for _, limit := range []uint64{0, 2, 1000, maxEventsPerSecondPerCPULimit} {
		if err := validateMaxEventsPerSecondPerCPU(limit); err != nil {
			t.Fatalf("validateMaxEventsPerSecondPerCPU(%d) error = %v", limit, err)
		}
	}

	for _, limit := range []uint64{1, maxEventsPerSecondPerCPULimit + 1} {
		err := validateMaxEventsPerSecondPerCPU(limit)
		if err == nil || !strings.Contains(err.Error(), "must be 0 or between") {
			t.Fatalf("validateMaxEventsPerSecondPerCPU(%d) error = %v", limit, err)
		}
	}
}

func newOutputFlagContext(t *testing.T, args ...string) *cli.Context {
	t.Helper()
	set := flag.NewFlagSet("irqtracing-test", flag.ContinueOnError)
	for _, cliFlag := range appFlags() {
		if err := cliFlag.Apply(set); err != nil {
			t.Fatalf("apply flag: %v", err)
		}
	}
	if err := set.Parse(args); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	app := cli.NewApp()
	app.ErrWriter = io.Discard
	return cli.NewContext(app, set, nil)
}
