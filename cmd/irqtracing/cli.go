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
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/urfave/cli/v2"

	profileroutput "github.com/ccfos/huatuo/internal/profiler/output"
)

// Older Clang versions emit the negative BTF enum as unsigned.
const allCPUsTarget int32 = -1

const (
	cliFlagBPFPath                  = "bpf-path"
	cliFlagTargetCPU                = "target-cpu"
	cliFlagDuration                 = "duration"
	cliFlagMaxEventsPerSecondPerCPU = "max-events-per-second-per-cpu"
	cliFlagOutput                   = "output"
	cliFlagOutputStorage            = "output-storage"
	cliFlagTaskID                   = "task-id"
	defaultMaxEventsPerSecondPerCPU = uint64(0)
	maxEventsPerSecondPerCPULimit   = uint64(math.MaxUint32) * 2
	maxDurationSeconds              = int64(math.MaxInt64) / int64(time.Second)
	outputText                      = "text"
	outputJSON                      = "json"
	outputCollapsed                 = string(profileroutput.FormatCollapsed)
	outputFlameGraph                = string(profileroutput.FormatFlameGraph)
	outputSVG                       = string(profileroutput.FormatSVG)
)

type irqTracingConfig struct {
	bpfPath                  string
	targetCPU                int32
	duration                 int
	maxEventsPerSecondPerCPU uint64
	output                   string
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
			Name:  cliFlagOutput,
			Value: outputText,
			Usage: "local output format: text|collapsed|flamegraph|svg|json (text aliases collapsed; flamegraph and svg emit SVG); ignored when --output-storage is set",
		},
		&cli.StringFlag{
			Name:  cliFlagOutputStorage,
			Usage: "Toolstream Unix socket path; requires --task-id and overrides local output",
		},
		&cli.StringFlag{
			Name:  cliFlagTaskID,
			Usage: "Toolstream task ID; requires --output-storage",
		},
	}
}

func validateFlags(c *cli.Context) error {
	if c.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %q", c.Args().Slice())
	}
	outputFormat := c.String(cliFlagOutput)
	switch outputFormat {
	case outputText, outputCollapsed, outputFlameGraph, outputSVG, outputJSON:
	default:
		return fmt.Errorf(
			"invalid --output %q; want text, collapsed, flamegraph, svg, or json",
			outputFormat,
		)
	}

	storage := c.String(cliFlagOutputStorage)
	taskID := c.String(cliFlagTaskID)
	if storage == "" && taskID != "" {
		return errors.New("--task-id requires --output-storage")
	}
	if storage != "" && taskID == "" {
		return errors.New("--task-id is required with --output-storage")
	}
	if storage != "" && c.IsSet(cliFlagOutput) {
		if _, err := fmt.Fprintln(c.App.ErrWriter,
			"warning: --output is ignored because --output-storage is set"); err != nil {
			return fmt.Errorf("write warning: %w", err)
		}
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
		output:                   c.String(cliFlagOutput),
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
