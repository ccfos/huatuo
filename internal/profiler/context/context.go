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

package context

import (
	"bytes"
	"context"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"huatuo-bamai/internal/profiler"
	"huatuo-bamai/internal/profiler/output"
	_ "huatuo-bamai/internal/profiler/output/flamegraph"
	_ "huatuo-bamai/internal/profiler/output/raw"
	psignal "huatuo-bamai/internal/profiler/signal"
	"huatuo-bamai/internal/toolstream"
	"huatuo-bamai/pkg/profiling"

	"github.com/urfave/cli/v2"
)

type ProfilerContext struct {
	Ctx    context.Context
	Cancel context.CancelFunc
	Cli    *cli.Context

	PIDs                 []int
	Freq                 int
	Duration             int
	MaxProfilerProcesses int
	AggrInterval         int
	IsOneShotAgg         bool
	CPUIDs               []int

	ServerAddress             string
	OutputFormat              output.OutputFormat
	OutputPath                string
	ContainerID               string
	Type                      profiling.Type
	Language                  profiling.Language
	ExecPath                  string
	Scope                     string
	ToolPath                  string
	LogBpfDebug               bool
	MemoryMode                profiling.MemoryMode
	PhysicalMemoryProbability uint

	TracerID string

	ToolstreamClient *toolstream.Client
}

type TracerData struct {
	MetricData any                   `json:"metric_data,omitempty"`
	FlameData  *profiler.ProfileData `json:"flamedata"`
}

func NewProfilerContext(cliCtx *cli.Context, logBuf *bytes.Buffer) (*ProfilerContext, error) {
	ctx, cancel := context.WithCancel(cliCtx.Context)

	sigCh, err := psignal.SetupSignals()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to setup signals: %w", err)
	}
	var cancelOnce sync.Once
	listenerDone := make(chan struct{})
	cancelProfiler := func() {
		cancelOnce.Do(func() {
			psignal.StopSignals(sigCh)
			cancel()
			<-listenerDone
		})
	}
	succeeded := false
	var tsClient *toolstream.Client
	defer func() {
		if succeeded {
			return
		}
		cancelProfiler()
		if tsClient != nil {
			_ = tsClient.Close()
		}
	}()

	go func() {
		defer close(listenerDone)
		sig, err := psignal.ListenSignalAndCancel(ctx, sigCh, cancel)
		if err != nil {
			fmt.Fprintf(logBuf, "[signal] error: %v\n", err)
		}
		if sig != nil {
			fmt.Fprintf(logBuf, "[signal] caught signal: %s, canceling context\n", sig)
		}
	}()

	outputFormat := output.OutputFormat(cliCtx.String("output-format"))

	tsClient, err = initToolstreamClient(cliCtx, outputFormat)
	if err != nil {
		return nil, err
	}

	var cpuIDs []int
	if cpuidStr := cliCtx.String("cpuid"); cpuidStr != "" {
		cpuIDs, err = parseCPUIDList(cpuidStr)
		if err != nil {
			return nil, err
		}
	}

	pids, err := ParsePIDs(cliCtx.String("pid"))
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
	mode := profiling.MemoryModeUnknown
	if cliCtx.String("memory-mode") != "" {
		mode, err = profiling.ParseMemoryMode(cliCtx.String("memory-mode"))
		if err != nil {
			return nil, err
		}
	}
	profilerContext := &ProfilerContext{
		Ctx:    ctx,
		Cancel: cancelProfiler,
		Cli:    cliCtx,

		PIDs:                 pids,
		Freq:                 cliCtx.Int("freq"),
		Duration:             cliCtx.Int("duration"),
		MaxProfilerProcesses: cliCtx.Int("max-concurrent-procs"),
		AggrInterval:         cliCtx.Int("aggr-interval"),
		CPUIDs:               cpuIDs,

		ServerAddress:             cliCtx.String("huatuo-api-address"),
		Type:                      typ,
		Language:                  language,
		ContainerID:               cliCtx.String("container-id"),
		ExecPath:                  cliCtx.String("binary-match-path"),
		Scope:                     cliCtx.String("scope"),
		ToolPath:                  cliCtx.String("tool-path"),
		LogBpfDebug:               cliCtx.Bool("log-bpf-debug"),
		OutputPath:                cliCtx.String("output-path"),
		OutputFormat:              outputFormat,
		MemoryMode:                mode,
		PhysicalMemoryProbability: cliCtx.Uint("physical-memory-probability"),

		TracerID: cliCtx.String("tracer-id"),

		ToolstreamClient: tsClient,
	}
	succeeded = true
	return profilerContext, nil
}

func initToolstreamClient(cliCtx *cli.Context, format output.OutputFormat) (*toolstream.Client, error) {
	if format != output.FormatRemote {
		return nil, nil
	}

	sockPath := cliCtx.String("output-storage")
	if sockPath == "" {
		return nil, fmt.Errorf("--output-storage is required when --output-format=remote")
	}

	client, err := toolstream.NewClient(toolstream.ClientOptions{
		SockPath: sockPath,
		ToolName: "profiler",
		Version:  "1",
	})
	if err != nil {
		return nil, fmt.Errorf("toolstream connect %s: %w", sockPath, err)
	}

	return client, nil
}

func parseCPUIDList(s string) ([]int, error) {
	numCPU := runtime.NumCPU()
	var cpuIDs []int
	seen := make(map[int]bool)

	parts := strings.Split(s, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		if strings.Contains(part, "-") {
			rangeParts := strings.Split(part, "-")
			if len(rangeParts) != 2 {
				return nil, fmt.Errorf("invalid cpuid range: %q", part)
			}

			start, err := strconv.Atoi(strings.TrimSpace(rangeParts[0]))
			if err != nil {
				return nil, fmt.Errorf("invalid cpuid range start: %q", rangeParts[0])
			}

			end, err := strconv.Atoi(strings.TrimSpace(rangeParts[1]))
			if err != nil {
				return nil, fmt.Errorf("invalid cpuid range end: %q", rangeParts[1])
			}

			if start > end {
				return nil, fmt.Errorf("invalid cpuid range: start %d > end %d", start, end)
			}

			for i := start; i <= end; i++ {
				if i < 0 || i >= numCPU {
					return nil, fmt.Errorf("cpuid %d is out of range (available: 0-%d)", i, numCPU-1)
				}
				if !seen[i] {
					seen[i] = true
					cpuIDs = append(cpuIDs, i)
				}
			}
		} else {
			id, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("invalid cpuid: %q", part)
			}

			if id < 0 || id >= numCPU {
				return nil, fmt.Errorf("cpuid %d is out of range (available: 0-%d)", id, numCPU-1)
			}

			if !seen[id] {
				seen[id] = true
				cpuIDs = append(cpuIDs, id)
			}
		}
	}

	if len(cpuIDs) == 0 {
		return nil, fmt.Errorf("cpuid list is empty")
	}

	return cpuIDs, nil
}
