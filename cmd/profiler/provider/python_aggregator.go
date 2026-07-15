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
	"strings"
	"sync"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler"
	"huatuo-bamai/internal/profiler/aggregator"
	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/output"
)

var _ aggregator.Aggregator = (*pythonAggregator)(nil)

type pythonAggregator struct {
	mu sync.Mutex

	formatter    output.Formatter
	sampleOutput []profiler.SampleOutput
	startedAt    time.Time
	sampleRate   int64
}

func newPythonCPUAggregator(pctx *pcontext.ProfilerContext) (*pythonAggregator, error) {
	pctx.IsOneShotAgg = true

	f, err := aggregator.NewFormatterForOutput(pctx)
	if err != nil {
		return nil, err
	}

	return &pythonAggregator{
		formatter:  f,
		startedAt:  time.Now(),
		sampleRate: int64(pctx.Freq),
	}, nil
}

func (a *pythonAggregator) Aggregate(rec any) {
	so, ok := rec.(profiler.SampleOutput)
	if !ok {
		log.Warnf("invalid record type %T, expected profiler.SampleOutput", rec)
		return
	}

	so.Output = normalizePythonOutput(so.PID, so.Output)
	if so.Output == "" {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	a.sampleOutput = append(a.sampleOutput, so)
	if a.formatter == nil {
		return
	}

	for _, line := range strings.Split(so.Output, "\n") {
		stack, count, ok := parseCollapsedLine(line)
		if !ok || count <= 0 {
			continue
		}

		if err := a.formatter.Add(&output.Sample{
			Frames: strings.Split(stack, ";"),
			Count:  count,
		}); err != nil {
			log.Warnf("formatter add sample: %v", err)
		}
	}
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

func (a *pythonAggregator) Snapshot(pctx *pcontext.ProfilerContext) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !pctx.OutputFormat.IsUpload() || len(a.sampleOutput) == 0 {
		return nil, nil
	}

	data, err := json.MarshalIndent(a.sampleOutput, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal sample output: %w", err)
	}

	pprofData, err := profiler.ParseRawData(
		pctx.Ctx,
		&profiler.ParseInput{
			StartTime:    a.startedAt,
			ProfileType:  profiler.ProfileTypeCpuSample,
			ProfilerName: "python-cpu",
			Data:         data,
			Opt:          &profiler.ParseOption{SampleRate: a.sampleRate},
			PID:          pctx.PID(),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to parse raw data: %w", err)
	}

	return pprofData, nil
}

func (a *pythonAggregator) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.formatter != nil {
		a.formatter.Reset()
	}
	a.sampleOutput = nil
}

func (a *pythonAggregator) OutputFormatter() output.Formatter {
	return a.formatter
}
