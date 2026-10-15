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

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/profiling/aggregator"
	"github.com/ccfos/huatuo/internal/profiling/profile"
	"github.com/ccfos/huatuo/pkg/types"
)

var _ aggregator.Aggregator = (*pythonAggregator)(nil)

type pythonAggregator struct {
	containerID  string
	pid          int
	mu           sync.Mutex
	sampleOutput []profile.SampleOutput
	sampleRate   int64
}

func newPythonCPUAggregator(cfg *Config) (*pythonAggregator, error) {
	return &pythonAggregator{
		containerID: cfg.ContainerID,
		pid:         cfg.PID(),
		sampleRate:  int64(cfg.Freq),
	}, nil
}

func (a *pythonAggregator) Aggregate(rec any) {
	so, ok := rec.(profile.SampleOutput)
	if !ok {
		log.Warnf("invalid record type %T, expected profile.SampleOutput", rec)
		return
	}

	so.Output = normalizePythonOutput(so.PID, so.Output)
	if so.Output == "" {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	a.sampleOutput = append(a.sampleOutput, so)
}

func normalizePythonOutput(pid int, raw string) string {
	var normalized strings.Builder
	for _, line := range strings.Split(raw, "\n") {
		stack, count, ok := parseCollapsedLine(line)
		if !ok || count <= 0 {
			continue
		}

		firstFrame, _, _ := strings.Cut(stack, ";")
		if !strings.HasPrefix(strings.TrimSpace(firstFrame), "process ") {
			fmt.Fprintf(&normalized, "process %d;%s %d\n", pid, stack, count)
			continue
		}
		fmt.Fprintf(&normalized, "%s %d\n", stack, count)
	}
	return normalized.String()
}

func (a *pythonAggregator) Snapshot(ctx context.Context) (*types.ProfilingWindow, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.sampleOutput) == 0 {
		data, err := profile.ParseTree(time.Time{}, profile.ProfileTypeCpuSample, nil, &profile.ParseOption{SampleRate: a.sampleRate})
		if err != nil {
			return nil, err
		}
		return profile.NewWindow(data, a.containerID), nil
	}

	data, err := json.MarshalIndent(a.sampleOutput, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal sample output: %w", err)
	}

	processNames, err := pythonProcessNames(ctx, a.sampleOutput, a.pid)
	if err != nil {
		return nil, fmt.Errorf("failed to parse raw data: %w", err)
	}

	pprofData, err := profile.ParseRawData(
		ctx,
		&profile.ParseInput{
			ProcessNames: processNames,
			ProfileType:  profile.ProfileTypeCpuSample,
			Data:         data,
			Opt:          &profile.ParseOption{SampleRate: a.sampleRate},
			PID:          a.pid,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to parse raw data: %w", err)
	}

	return profile.NewWindow(pprofData, a.containerID), nil
}

func (a *pythonAggregator) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.sampleOutput = nil
}
