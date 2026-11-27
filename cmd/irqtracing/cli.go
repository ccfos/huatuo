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

	profcli "github.com/ccfos/huatuo/internal/profiling/cli"
	profileroutput "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/internal/utils/cpuutil"
	"github.com/ccfos/huatuo/pkg/profiling"
)

// Older Clang versions emit the negative BTF enum as unsigned.
const allCPUsTarget int32 = -1

const (
	cliFlagBPFPath                  = "bpf-path"
	cliFlagCPUList                  = "cpu-list"
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
	cpuID                    int32
	duration                 int
	maxEventsPerSecondPerCPU uint64
	output                   profileroutput.OutputFormat
}

func appFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:  cliFlagBPFPath,
			Usage: "path to the irqtracing BPF object file (required for collection)",
		},
		&cli.StringFlag{
			Name:  cliFlagCPUList,
			Usage: "CPU list to trace; currently supports one CPU, empty for all CPUs",
		},
		&cli.IntFlag{
			Name:  cliFlagDuration,
			Value: 3,
			Usage: "Collection duration in seconds",
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
			Usage: "Output format: collapsed (folded stacks), flamegraph (SVG, alias: svg), tui, json, remote-store",
		},
		profcli.TUIOpenFlag(),
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

	return validateOutputFlags(c)
}

func validateOutputFlags(c *cli.Context) error {
	format := profileroutput.OutputFormat(c.String(cliFlagOutputFormat))
	path := c.String(cliFlagOutputPath)
	if format == profileroutput.FormatTUI {
		if err := profileroutput.ValidateTUIDestination(path); err != nil {
			return err
		}
		return profileroutput.ValidateTUITerminal()
	}

	if !c.IsSet(cliFlagOutputPath) || !c.IsSet(cliFlagOutputFormat) {
		return fmt.Errorf("--output-path and --output-format must both be specified")
	}
	if err := profileroutput.ValidateDestination(path); err != nil {
		return err
	}

	switch format {
	case profileroutput.FormatCollapsed, profileroutput.FormatFlameGraph, profileroutput.FormatSVG,
		profileroutput.FormatJSON, profileroutput.FormatRemoteStore:
	default:
		return fmt.Errorf("unsupported output format %q", format)
	}
	if format == profileroutput.FormatRemoteStore && c.String(cliFlagTaskID) == "" {
		return fmt.Errorf("--task-id must not be empty when --output-format=remote-store")
	}

	return nil
}

func loadConfig(c *cli.Context) (irqTracingConfig, error) {
	cpuID, err := parseCPUListID(c.String(cliFlagCPUList))
	if err != nil {
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
		cpuID:                    cpuID,
		duration:                 duration,
		maxEventsPerSecondPerCPU: limit,
		output:                   profileroutput.OutputFormat(c.String(cliFlagOutputFormat)),
	}, nil
}

func parseCPUListID(list string) (int32, error) {
	spec := profiling.Spec{
		Type:        profiling.TypeIRQ,
		HostCPUList: list,
	}
	ids, err := spec.ParseHostCPUList()
	if err != nil {
		return 0, fmt.Errorf("--cpu-list: %w", err)
	}
	if err := cpuutil.ValidateOnline(ids, cpuutil.SystemCPUOnlinePath); err != nil {
		return 0, fmt.Errorf("--cpu-list: %w", err)
	}
	if len(ids) == 0 {
		return allCPUsTarget, nil
	}
	return int32(ids[0]), nil
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
