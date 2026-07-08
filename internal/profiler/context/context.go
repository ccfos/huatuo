// Copyright 2025 The HuaTuo Authors
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
	"encoding/json"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"huatuo-bamai/internal/profiler"
	"huatuo-bamai/internal/profiler/output"
	_ "huatuo-bamai/internal/profiler/output/flamegraph"
	_ "huatuo-bamai/internal/profiler/output/raw"
	psignal "huatuo-bamai/internal/profiler/signal"
	"huatuo-bamai/internal/toolstream"

	"github.com/urfave/cli/v2"
)

type ProfilerContext struct {
	Ctx    context.Context
	Cancel context.CancelFunc
	Cli    *cli.Context

	PID          int
	Freq         int
	Duration     int
	ToolLimit    int
	AggrInterval int
	IsOneShotAgg bool
	CPUIDs       []int

	ServerAddress string
	OutputFormat  output.OutputFormat
	OutputPath    string
	ContainerID   string
	Type          string
	Language      string
	ExecPath      string
	Scope         string
	ToolPath      string
	LogBpfDebug   bool

	ExtraFlags      map[string]string
	MetaData        map[string]string
	CpuIdleMetaData map[string]int64
	CpuSysMetaData  map[string]int64

	ToolstreamClient *toolstream.Client
}

type TracerData struct {
	MetricData any                   `json:"metric_data,omitempty"`
	MetaData   any                   `json:"metadata,omitempty"`
	FlameData  *profiler.ProfileData `json:"flamedata"`
}

func NewProfilerContext(cliCtx *cli.Context, logBuf *bytes.Buffer) (*ProfilerContext, error) {
	ctx, cancel := context.WithCancel(cliCtx.Context)

	sigCh, err := psignal.SetupSignals()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to setup signals: %w", err)
	}

	go func() {
		sig, err := psignal.ListenSignalAndCancel(sigCh, cancel)
		if err != nil {
			fmt.Fprintf(logBuf, "[signal] error: %v\n", err)
		}
		fmt.Fprintf(logBuf, "[signal] caught signal: %s, canceling context\n", sig)
	}()

	flagsMap, err := parseExtraFlagsString(cliCtx.StringSlice("flags"))
	if err != nil {
		return nil, err
	}

	metaData, err := parseExtraFlagsString(cliCtx.StringSlice("metadata"))
	if err != nil {
		return nil, err
	}
	if mockContainer := cliCtx.String("mock-container"); mockContainer != "" {
		metaData["mock_container"] = mockContainer
	}

	cpuidleMeta, err := parseExtraFlagsInt64(cliCtx.StringSlice("cpuidle-metadata"))
	if err != nil {
		return nil, err
	}

	cpusysMeta, err := parseExtraFlagsInt64(cliCtx.StringSlice("cpusys-metadata"))
	if err != nil {
		return nil, err
	}

	outputFormat := output.OutputFormat(cliCtx.String("output-format"))

	tsClient, err := initToolstreamClient(cliCtx, outputFormat)
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

	return &ProfilerContext{
		Ctx:    ctx,
		Cancel: cancel,
		Cli:    cliCtx,

		PID:          cliCtx.Int("pid"),
		Freq:         cliCtx.Int("freq"),
		Duration:     cliCtx.Int("duration"),
		ToolLimit:    cliCtx.Int("tool-limit"),
		AggrInterval: cliCtx.Int("aggr-interval"),
		CPUIDs:       cpuIDs,

		ServerAddress: cliCtx.String("server-address"),
		Type:          cliCtx.String("type"),
		Language:      cliCtx.String("language"),
		ContainerID:   cliCtx.String("container-id"),
		ExecPath:      cliCtx.String("exec-path"),
		Scope:         cliCtx.String("scope"),
		ToolPath:      cliCtx.String("tool-path"),
		LogBpfDebug:   cliCtx.Bool("log-bpf-debug"),
		OutputPath:    cliCtx.String("output-path"),
		OutputFormat:  outputFormat,

		MetaData:        metaData,
		ExtraFlags:      flagsMap,
		CpuSysMetaData:  cpusysMeta,
		CpuIdleMetaData: cpuidleMeta,

		ToolstreamClient: tsClient,
	}, nil
}

func MapToStructByJSON[T any](m map[string]int64) (T, error) {
	var meta T

	data, err := json.Marshal(m)
	if err != nil {
		return meta, err
	}

	err = json.Unmarshal(data, &meta)
	return meta, err
}

type flagSegment struct {
	key   string
	value string
}

func parseFlagSegments(flagList []string) ([]flagSegment, error) {
	var segments []flagSegment

	for _, raw := range flagList {
		for _, segment := range strings.Split(raw, ",") {
			segment = strings.TrimSpace(segment)
			if segment == "" {
				continue
			}

			clean := strings.TrimLeft(segment, "-")

			if strings.Contains(clean, "=") {
				parts := strings.SplitN(clean, "=", 2)
				key := strings.TrimSpace(parts[0])
				value := strings.TrimSpace(parts[1])
				if key != "" {
					segments = append(segments, flagSegment{key: key, value: value})
				}

				continue
			}

			parts := strings.Fields(clean)
			if len(parts) == 2 {
				key := strings.TrimSpace(parts[0])
				value := strings.TrimSpace(parts[1])
				if key != "" {
					segments = append(segments, flagSegment{key: key, value: value})
				}

				continue
			}

			return nil, fmt.Errorf("invalid extra flag format: %q (expected --key=value or --key value)", segment)
		}
	}

	return segments, nil
}

func parseExtraFlagsString(flagList []string) (map[string]string, error) {
	segments, err := parseFlagSegments(flagList)
	if err != nil {
		return nil, err
	}

	flags := make(map[string]string, len(segments))
	for _, s := range segments {
		flags[s.key] = s.value
	}

	return flags, nil
}

func parseExtraFlagsInt64(flagList []string) (map[string]int64, error) {
	segments, err := parseFlagSegments(flagList)
	if err != nil {
		return nil, err
	}

	flags := make(map[string]int64, len(segments))
	for _, s := range segments {
		iValue, err := strconv.Atoi(s.value)
		if err != nil {
			return nil, fmt.Errorf("invalid int64 flag value for %q: %w", s.key, err)
		}

		flags[s.key] = int64(iValue)
	}

	return flags, nil
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
