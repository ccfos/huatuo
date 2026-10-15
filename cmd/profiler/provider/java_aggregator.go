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
	"sync"
	"time"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/profiler"
	"github.com/ccfos/huatuo/internal/profiler/aggregator"
	"github.com/ccfos/huatuo/pkg/types"
)

// Compile-time check: javaAggregator implements aggregator.Aggregator.
var _ aggregator.Aggregator = (*javaAggregator)(nil)

type javaAggregator struct {
	containerID  string
	pid          int
	parseOption  *profiler.ParseOption
	sampleType   string
	mu           sync.Mutex
	sampleOutput []profiler.SampleOutput
}

func newJavaAggregator(cfg *Config) (*javaAggregator, error) {
	opt, sampleType, err := profileTypeOptions(cfg)
	if err != nil {
		return nil, err
	}
	return &javaAggregator{
		containerID: cfg.ContainerID,
		pid:         cfg.PID(),
		parseOption: opt,
		sampleType:  sampleType,
	}, nil
}

func (a *javaAggregator) Aggregate(rec any) {
	so, ok := rec.(profiler.SampleOutput)
	if !ok {
		log.Warnf("invalid record type %T, expected profiler.SampleOutput", rec)

		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	a.sampleOutput = append(a.sampleOutput, so)
}

func (a *javaAggregator) Snapshot(ctx context.Context) (*types.ProfilingWindow, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.snapshotPprof(ctx)
}

func (a *javaAggregator) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.sampleOutput = nil
}

func (a *javaAggregator) snapshotPprof(ctx context.Context) (*types.ProfilingWindow, error) {
	if len(a.sampleOutput) == 0 {
		data, err := profiler.ParseTree(time.Time{}, a.sampleType, nil, a.parseOption)
		if err != nil {
			return nil, err
		}
		return profiler.NewWindow(data, a.containerID)
	}

	pprofFolded, err := json.MarshalIndent(a.sampleOutput, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal sample output: %w", err)
	}

	pprofData, err := profiler.ParseCollapsedData(
		ctx,
		&profiler.ParseInput{
			ProfileType: a.sampleType,
			Data:        pprofFolded,
			Opt:         a.parseOption,
			PID:         a.pid,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to parse collapsed data: %w", err)
	}

	return profiler.NewWindow(pprofData, a.containerID)
}
