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
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/profiler"
	"github.com/ccfos/huatuo/internal/profiler/aggregator"
	pcontext "github.com/ccfos/huatuo/internal/profiler/context"
	"github.com/ccfos/huatuo/pkg/profiling"
	"github.com/ccfos/huatuo/pkg/types"
)

// Compile-time check: javaAggregator implements aggregator.Aggregator.
var _ aggregator.Aggregator = (*javaAggregator)(nil)

type javaAggregator struct {
	mu           sync.Mutex
	sampleOutput []profiler.SampleOutput
	startedAt    time.Time
}

func newJavaAggregator(pctx *pcontext.ProfilerContext) (*javaAggregator, error) {
	pctx.IsOneShotAgg = true

	return &javaAggregator{
		startedAt: time.Now(),
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

func (a *javaAggregator) Snapshot(pctx *pcontext.ProfilerContext) (*types.ProfilingWindow, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.snapshotPprof(pctx)
}

func (a *javaAggregator) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.sampleOutput = nil
}

func (a *javaAggregator) snapshotPprof(pctx *pcontext.ProfilerContext) (*types.ProfilingWindow, error) {
	if len(a.sampleOutput) == 0 {
		opt, sampleType, _, err := javaParseOptions(pctx)
		if err != nil {
			return nil, err
		}
		data, err := profiler.ParseTree(a.startedAt, sampleType, nil, opt)
		if err != nil {
			return nil, err
		}
		return profiler.NewWindow(data, pctx.ContainerID, time.Since(a.startedAt))
	}

	pprofFolded, err := json.MarshalIndent(a.sampleOutput, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal sample output: %w", err)
	}

	opt, sampleType, prName, err := javaParseOptions(pctx)
	if err != nil {
		return nil, err
	}

	pprofData, err := profiler.ParseCollapsedData(
		pctx.Ctx,
		&profiler.ParseInput{
			StartTime:    a.startedAt,
			ProfileType:  sampleType,
			ProfilerName: prName,
			Data:         pprofFolded,
			Opt:          opt,
			PID:          pctx.PID(),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to parse collapsed data: %w", err)
	}

	return profiler.NewWindow(pprofData, pctx.ContainerID, time.Since(a.startedAt))
}

func javaParseOptions(pctx *pcontext.ProfilerContext) (*profiler.ParseOption, string, string, error) {
	opt, sampleType, err := profileTypeOptions(pctx)
	if err != nil {
		return nil, "", "", err
	}

	prName := "java-" + string(pctx.Type)
	if pctx.Type == profiling.TypeMemory {
		prName = "java-mem"
	}
	return opt, sampleType, prName, nil
}
