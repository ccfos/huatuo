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
	"strconv"
	"strings"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/ccfos/huatuo/cmd/profiler/provider"
	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/pkg/profiling"
)

type commandConfig struct {
	Provider provider.Config
	Language profiling.Language
	Output   output.Config
	TaskID   string
}

func loadConfig(cliCtx *cli.Context) (*commandConfig, error) {
	duration, err := parseSeconds("duration", cliCtx.Int("duration"))
	if err != nil {
		return nil, err
	}
	interval, err := parseSeconds("aggr-interval", cliCtx.Int("aggr-interval"))
	if err != nil {
		return nil, err
	}
	outputFormat := output.OutputFormat(cliCtx.String("output-format"))

	outputPath := cliCtx.String("output-path")
	var cpuIDs []int
	if cpuidStr := cliCtx.String("cpuid"); cpuidStr != "" {
		cpuIDs, err = parseCPUIDs(cpuidStr)
		if err != nil {
			return nil, err
		}
	}

	pids, err := parsePIDs(cliCtx.String("pid"))
	if err != nil {
		return nil, err
	}
	typ, err := profiling.ParseType(cliCtx.String("type"))
	if err != nil {
		return nil, err
	}
	language, err := profiling.ParseLanguage(cliCtx.String("language"))
	if err != nil {
		return nil, err
	}
	modeValue := cliCtx.String("cpu-mode")
	if typ == profiling.TypeMemory {
		modeValue = cliCtx.String("memory-mode")
	} else if modeValue == "" {
		modeValue = string(profiling.ModeOnCPU)
	}
	mode, err := profiling.ParseMode(modeValue)
	if err != nil {
		return nil, err
	}
	offCPUPhaseValue := cliCtx.String("offcpu-phase")
	if offCPUPhaseValue == "" {
		offCPUPhaseValue = string(profiling.OffCPUPhaseAll)
	}
	offCPUPhase, err := profiling.ParseOffCPUPhase(offCPUPhaseValue)
	if err != nil {
		return nil, err
	}
	cfg := &commandConfig{
		Provider: provider.Config{
			PIDs:                 pids,
			Freq:                 cliCtx.Int("freq"),
			Duration:             duration,
			MaxProfilerProcesses: cliCtx.Int("max-concurrent-procs"),
			AggregationInterval:  interval,
			CPUIDs:               cpuIDs,
			RequireHardwarePMU:   cliCtx.Bool("require-hardware-pmu"),

			ServerAddress:             cliCtx.String("huatuo-api-address"),
			Type:                      typ,
			ContainerID:               cliCtx.String("container-id"),
			ExecPath:                  cliCtx.String("binary-match-path"),
			ThreadGroup:               cliCtx.Bool("thread-group"),
			ToolDir:                   cliCtx.String("tool-path"),
			LogBpfDebug:               cliCtx.Bool("log-bpf-debug"),
			Mode:                      mode,
			OffCPUPhase:               offCPUPhase,
			OffCPUMinDurationUS:       cliCtx.Uint64("offcpu-min-duration-us"),
			OffCPUStatsEnabled:        cliCtx.Bool("offcpu-stats"),
			PhysicalMemoryProbability: cliCtx.Uint("physical-memory-probability"),
		},
		Language: language,
		Output:   output.Config{Format: outputFormat, Destination: outputPath},
		TaskID:   cliCtx.String("task-id"),
	}
	return cfg, nil
}

// parsePIDs parses a comma-separated list of positive process IDs.
func parsePIDs(value string) ([]int, error) {
	if value == "" {
		return nil, nil
	}

	parts := strings.Split(value, ",")
	pids := make([]int, 0, len(parts))
	seen := make(map[int]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		pid, err := strconv.Atoi(part)
		if err != nil || pid <= 0 {
			return nil, fmt.Errorf("invalid --pid value %q: expected positive comma-separated PIDs", value)
		}
		if _, ok := seen[pid]; ok {
			return nil, fmt.Errorf("invalid --pid value %q: PID %d is duplicated", value, pid)
		}

		seen[pid] = struct{}{}
		pids = append(pids, pid)
	}

	return pids, nil
}

func parseSeconds(name string, seconds int) (time.Duration, error) {
	if seconds < 1 || int64(seconds) > math.MaxInt64/int64(time.Second) {
		return 0, fmt.Errorf("--%s must be between 1 and %d seconds", name, math.MaxInt64/int64(time.Second))
	}
	return time.Duration(seconds) * time.Second, nil
}
