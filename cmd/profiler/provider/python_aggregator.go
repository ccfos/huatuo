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
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler"
	"huatuo-bamai/internal/profiler/aggregator"
	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/output"
	"huatuo-bamai/internal/profiler/output/raw"
)

// Compile-time check: pythonAggregator implements aggregator.Aggregator.
var _ aggregator.Aggregator = (*pythonAggregator)(nil)

type pythonAggregator struct {
	mu sync.Mutex

	folded           *raw.Formatter
	sampleOutput     []profiler.SampleOutput
	keepSampleOutput bool
	profilerName     string
	profileType      string
	sampleRate       int64
}

func newPythonAggregator(pctx *pcontext.ProfilerContext, name, typ string, rate int64) *pythonAggregator {
	return &pythonAggregator{
		profilerName:     name,
		profileType:      typ,
		sampleRate:       rate,
		folded:           raw.New(),
		keepSampleOutput: (pctx.OutputFormat == "pprof" || pctx.OutputFormat == "es") && !pctx.OneShotAgg,
	}
}

func newPythonCPUAggregator(pctx *pcontext.ProfilerContext) *pythonAggregator {
	return newPythonAggregator(pctx, "python-cpu", profiler.ProfileTypeCpuSample, int64(pctx.Freq))
}

func (a *pythonAggregator) Aggregate(rec any) {
	so, ok := rec.(profiler.SampleOutput)
	if !ok {
		log.Infof("invalid record type %T, expected profiler.SampleOutput", rec)

		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.keepSampleOutput {
		a.sampleOutput = append(a.sampleOutput, profiler.SampleOutput{
			PID:    so.PID,
			Output: so.Output,
		})
	}

	lines := strings.Split(so.Output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		idx := strings.LastIndex(line, " ")
		if idx == -1 {
			continue
		}

		stack := line[:idx]
		countStr := strings.TrimSpace(line[idx+1:])
		count, err := strconv.ParseInt(countStr, 10, 64)
		if err != nil {
			continue
		}

		frames := strings.Split(stack, ";")
		_ = a.folded.Add(&output.Sample{Frames: frames, Count: count})
	}
}

func (a *pythonAggregator) Snapshot(pctx *pcontext.ProfilerContext) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.folded.IsEmpty() {
		return nil, nil
	}

	if pctx.OutputFormat != "pprof" && pctx.OutputFormat != "es" {
		// Emit per-interval deltas so the pipeline avoids double-aggregation
		// when alloc/free span different intervals.
		a.folded.Reset()

		return nil, nil
	}

	return a.snapshotPprof(pctx)
}

func (a *pythonAggregator) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.folded.Reset()
	a.sampleOutput = nil
}

func (a *pythonAggregator) FoldedFormatter() *raw.Formatter {
	return a.folded
}

func (a *pythonAggregator) snapshotPprof(pctx *pcontext.ProfilerContext) (any, error) {
	var folded bytes.Buffer

	if pctx.OneShotAgg {
		negatives := 0
		var negativeSamples []string

		for stack, count := range a.folded.Counts() {
			if count < 0 {
				negatives++
				if len(negativeSamples) < 5 {
					negativeSamples = append(negativeSamples, fmt.Sprintf("%s %d", stack, count))
				}

				continue
			}

			fmt.Fprintf(&folded, "%s %d\n", stack, count)
		}

		if negatives > 0 {
			log.P().Infof("[profiler] %s one-shot: dropped negatives=%d sample=%s", a.profilerName, negatives, strings.Join(negativeSamples, " | "))
		}

		if folded.Len() == 0 {
			log.P().Infof("[profiler] %s one-shot: no non-negative samples after filtering", a.profilerName)

			return nil, nil
		}

		pprofFolded, err := json.MarshalIndent([]profiler.SampleOutput{
			{PID: pctx.PID, Output: folded.String()},
		}, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("failed to marshal sample output: %w", err)
		}

		opt := &profiler.ParseOption{SampleRate: a.sampleRate}
		pprofData, err := profiler.ParseRawData(
			pctx.Ctx,
			time.Now(),
			a.profileType,
			a.profilerName,
			pprofFolded,
			opt,
			pctx.PID,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to parse raw data: %w", err)
		}

		return pprofData, nil
	}

	if len(a.sampleOutput) == 0 {
		return nil, nil
	}

	pprofFolded, err := json.MarshalIndent(a.sampleOutput, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal sample output: %w", err)
	}

	opt := &profiler.ParseOption{SampleRate: a.sampleRate}
	pprofData, err := profiler.ParseRawData(
		pctx.Ctx,
		time.Now(),
		a.profileType,
		a.profilerName,
		pprofFolded,
		opt,
		pctx.PID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to parse raw data: %w", err)
	}

	return pprofData, nil
}
