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
	"fmt"
	"math"
	"time"

	"github.com/urfave/cli/v2"

	profileroutput "github.com/ccfos/huatuo/internal/profiling/formatter"
)

// Older Clang versions emit the negative BTF enum as unsigned.
const allCPUsTarget int32 = -1

const (
	cliFlagBPFPath                  = "bpf-path"
	cliFlagTargetCPU                = "target-cpu"
	cliFlagDuration                 = "duration"
	cliFlagMaxEventsPerSecondPerCPU = "max-events-per-second-per-cpu"
	cliFlagOutputFormat             = "output-format"
	cliFlagOutputPath               = "output-path"
	cliFlagTaskID                   = "task-id"
	cliFlagEnablePProf              = "enable-pprof"
	defaultMaxEventsPerSecondPerCPU = uint64(0)
	maxEventsPerSecondPerCPULimit   = uint64(math.MaxUint32) * 2
	maxDurationSeconds              = int64(math.MaxInt64) / int64(time.Second)
)

type irqTracingConfig struct {
	bpfPath                  string
	targetCPU                int32
	duration                 int
	maxEventsPerSecondPerCPU uint64
	output                   profileroutput.OutputFormat
}

func appFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:     cliFlagBPFPath,
			Usage:    "path to the irqtracing BPF object file",
			Required: true,
		},
		&cli.Int64Flag{
			Name:  cliFlagTargetCPU,
			Value: int64(allCPUsTarget),
			Usage: "cpu to trace; omit or set to -1 to trace all CPUs",
		},
		&cli.IntFlag{
			Name:  cliFlagDuration,
			Value: 3,
			Usage: "collect duration in seconds",
		},
		&cli.Uint64Flag{
			Name:  cliFlagMaxEventsPerSecondPerCPU,
			Value: defaultMaxEventsPerSecondPerCPU,
			Usage: "limit source and victim stack samples to N events/sec per traced CPU (0 = unlimited)",
		},
		&cli.StringFlag{
			Name:  cliFlagOutputPath,
			Usage: "Output path: file|directory|socket|stdout (WARNING: stdout may expose data)",
		},
		&cli.StringFlag{
			Name:  cliFlagOutputFormat,
			Value: string(profileroutput.FormatCollapsed),
			Usage: "Output format: collapsed|flamegraph|svg|json|remote-store",
		},
		&cli.StringFlag{
			Name:  cliFlagTaskID,
			Usage: "Profiling task ID; required when --output-format=remote-store",
		},
		&cli.BoolFlag{
			Name:  cliFlagEnablePProf,
			Usage: "serve this process's Go runtime profiles on 127.0.0.1:6000",
		},
	}
}

func validateFlags(c *cli.Context) error {
	if c.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %q", c.Args().Slice())
	}
	if !c.IsSet(cliFlagOutputPath) || !c.IsSet(cliFlagOutputFormat) {
		return fmt.Errorf("--output-path and --output-format must both be specified")
	}
	path := c.String(cliFlagOutputPath)
	if err := profileroutput.ValidateDestination(path); err != nil {
		return err
	}
	format := profileroutput.OutputFormat(c.String(cliFlagOutputFormat))
	switch format {
	case profileroutput.FormatCollapsed, profileroutput.FormatFlameGraph, profileroutput.FormatSVG, profileroutput.FormatJSON, profileroutput.FormatRemoteStore:
	default:
		return fmt.Errorf("unsupported output format %q", format)
	}
	if format == profileroutput.FormatRemoteStore && c.String(cliFlagTaskID) == "" {
		return fmt.Errorf("--task-id must not be empty when --output-format=remote-store")
	}

	return nil
}

func loadConfig(c *cli.Context) (irqTracingConfig, error) {
	targetCPU := c.Int64(cliFlagTargetCPU)
	if err := validateTargetCPU(targetCPU); err != nil {
		return irqTracingConfig{}, err
	}

	duration := c.Int(cliFlagDuration)
	if err := validateDuration(duration); err != nil {
		return irqTracingConfig{}, err
	}

	limit := c.Uint64(cliFlagMaxEventsPerSecondPerCPU)
	if err := validateMaxEventsPerSecondPerCPU(limit); err != nil {
		return irqTracingConfig{}, err
	}

	return irqTracingConfig{
		bpfPath:                  c.String(cliFlagBPFPath),
		targetCPU:                int32(targetCPU),
		duration:                 duration,
		maxEventsPerSecondPerCPU: limit,
		output:                   profileroutput.OutputFormat(c.String(cliFlagOutputFormat)),
	}, nil
}

func validateTargetCPU(targetCPU int64) error {
	if targetCPU < int64(allCPUsTarget) || targetCPU > math.MaxInt32 {
		return fmt.Errorf("--target-cpu must be -1 or between 0 and %d", math.MaxInt32)
	}
	return nil
}

func validateDuration(duration int) error {
	if duration <= 0 || int64(duration) > maxDurationSeconds {
		return fmt.Errorf("--duration must be between 1 and %d seconds", maxDurationSeconds)
	}
	return nil
}

func validateMaxEventsPerSecondPerCPU(limit uint64) error {
	if limit == 0 {
		return nil
	}
	if limit < 2 || limit > maxEventsPerSecondPerCPULimit {
		return fmt.Errorf("--%s must be 0 or between 2 and %d",
			cliFlagMaxEventsPerSecondPerCPU, maxEventsPerSecondPerCPULimit)
	}
	return nil
}
