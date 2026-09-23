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
	"bytes"
	"flag"
	"io"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/urfave/cli/v2"

	bpfabi "github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/toolstream"
	"github.com/ccfos/huatuo/internal/version"
)

func TestValidateFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "local output"},
		{name: "JSON output", args: []string{"--output", "json"}},
		{
			name: "Toolstream output",
			args: []string{"--output-storage", "/run/toolstream.sock", "--task-id", "task-1"},
		},
		{name: "collapsed output", args: []string{"--output", "collapsed"}},
		{name: "flame graph output", args: []string{"--output", "flamegraph"}},
		{name: "SVG output", args: []string{"--output", "svg"}},
		{
			name:    "invalid output",
			args:    []string{"--output", "folded"},
			wantErr: "want text, collapsed, flamegraph, svg, or json",
		},
		{
			name:    "missing task ID",
			args:    []string{"--output-storage", "/run/toolstream.sock"},
			wantErr: "--task-id is required",
		},
		{
			name:    "task ID without Toolstream",
			args:    []string{"--task-id", "task-1"},
			wantErr: "--task-id requires --output-storage",
		},
		{
			name:    "unexpected argument",
			args:    []string{"extra"},
			wantErr: "unexpected arguments",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := newOutputFlagContext(t, test.args...)
			err := validateFlags(ctx)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("validateFlags() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("validateFlags() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestValidateFlagsWarnsWhenStorageOverridesOutput(t *testing.T) {
	ctx := newOutputFlagContext(t,
		"--output", "json",
		"--output-storage", "/run/toolstream.sock",
		"--task-id", "task-1",
	)
	var stderr bytes.Buffer
	ctx.App.ErrWriter = &stderr

	if err := validateFlags(ctx); err != nil {
		t.Fatalf("validateFlags() error = %v", err)
	}
	if got := stderr.String(); got != "warning: --output is ignored because --output-storage is set\n" {
		t.Fatalf("warning = %q", got)
	}
}

func TestLoadConfig(t *testing.T) {
	ctx := newOutputFlagContext(t,
		"--bpf-path", "/tmp/irqtracing.o",
		"--target-cpu", "4",
		"--duration", "5",
		"--max-events-per-second-per-cpu", "1000",
		"--output", "json",
	)

	cfg, err := loadConfig(ctx)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.bpfPath != "/tmp/irqtracing.o" || cfg.targetCPU != 4 ||
		cfg.duration != 5 || cfg.maxEventsPerSecondPerCPU != 1000 ||
		cfg.output != outputJSON {
		t.Fatalf("loadConfig() = %+v", cfg)
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

func TestIRQTracingBPFConstants(t *testing.T) {
	t.Run("unlimited", func(t *testing.T) {
		constants := irqTracingBPFConstants(allCPUsTarget, 0)
		if len(constants) != 1 || constants["target_cpu"] != allCPUsTarget {
			t.Fatalf("constants = %#v, want only target_cpu", constants)
		}
	})

	t.Run("splits per-CPU limit", func(t *testing.T) {
		constants := irqTracingBPFConstants(3, 1000)
		if got := constants[sourceRateLimitConstant]; got != uint32(500) {
			t.Fatalf("source rate = %v, want 500", got)
		}
		if got := constants[victimRateLimitConstant]; got != uint32(500) {
			t.Fatalf("victim rate = %v, want 500", got)
		}
	})

	t.Run("keeps odd total", func(t *testing.T) {
		constants := irqTracingBPFConstants(3, 1001)
		if got := constants[sourceRateLimitConstant]; got != uint32(501) {
			t.Fatalf("source rate = %v, want 501", got)
		}
		if got := constants[victimRateLimitConstant]; got != uint32(500) {
			t.Fatalf("victim rate = %v, want 500", got)
		}
	})

	t.Run("keeps maximum total", func(t *testing.T) {
		constants := irqTracingBPFConstants(3, maxEventsPerSecondPerCPULimit)
		if got := constants[sourceRateLimitConstant]; got != uint32(math.MaxUint32) {
			t.Fatalf("source rate = %v, want %d", got, uint32(math.MaxUint32))
		}
		if got := constants[victimRateLimitConstant]; got != uint32(math.MaxUint32) {
			t.Fatalf("victim rate = %v, want %d", got, uint32(math.MaxUint32))
		}
	})
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

func TestWriteDroppedSamplesWarning(t *testing.T) {
	var output bytes.Buffer
	writeDroppedSamplesWarning(&output, 7)

	if got := output.String(); got != "irqtracing: 7 samples dropped; the flame graph is incomplete\n" {
		t.Fatalf("warning = %q", got)
	}
}

func TestOpenToolstreamSendsResult(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "toolstream.sock")
	server, err := toolstream.NewServer(sockPath)
	if err != nil {
		t.Fatalf("toolstream.NewServer() error = %v", err)
	}
	received := make(chan *IRQTracingResult, 1)
	toolstream.Register(server, irqTracingToolName, func(sess *toolstream.Session, result *IRQTracingResult) error {
		if sess.TaskID != "task-1" {
			t.Errorf("TaskID = %q, want task-1", sess.TaskID)
		}
		received <- result
		return nil
	})
	if err := server.Start(); err != nil {
		t.Fatalf("server.Start() error = %v", err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Errorf("server.Close() error = %v", err)
		}
	})

	previousVersion := versionInfo
	versionInfo = version.Info{Version: "test"}
	t.Cleanup(func() { versionInfo = previousVersion })

	ctx := newOutputFlagContext(t, "--output-storage", sockPath, "--task-id", "task-1")
	client, err := openToolstream(ctx)
	if err != nil {
		t.Fatalf("openToolstream() error = %v", err)
	}
	result := &IRQTracingResult{NMissed: 3}
	outputWriter, err := newWriter(io.Discard, outputText, client)
	if err != nil {
		t.Fatalf("newWriter() error = %v", err)
	}
	if err := outputWriter.Write(&irqTracingSnapshot{result: result}); err != nil {
		t.Fatalf("socket writer error = %v", err)
	}
	if err := client.End(); err != nil {
		t.Fatalf("client.End() error = %v", err)
	}

	select {
	case got := <-received:
		if got.NMissed != 3 {
			t.Fatalf("received result = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Toolstream result")
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
