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

	formatter              output.Formatter
	sampleOutput           []profiler.SampleOutput
	shouldKeepSampleOutput bool
	profilerName           string
	profileType            string
	sampleRate             int64
}

func newPythonAggregator(pctx *pcontext.ProfilerContext, name, typ string, rate int64) (*pythonAggregator, error) {
	var f output.Formatter

	if pctx.OutputFormat.IsUpload() {
		if pctx.IsOneShotAgg {
			f = raw.New()
		}
	} else {
		var err error
		f, err = aggregator.NewFormatterForOutput(pctx)
		if err != nil {
			return nil, err
		}
	}

	return &pythonAggregator{
		profilerName:           name,
		profileType:            typ,
		sampleRate:             rate,
		formatter:              f,
		shouldKeepSampleOutput: pctx.OutputFormat.IsUpload() && !pctx.IsOneShotAgg,
	}, nil
}

func newPythonCPUAggregator(pctx *pcontext.ProfilerContext) (*pythonAggregator, error) {
	return newPythonAggregator(pctx, "python-cpu", profiler.ProfileTypeCpuSample, int64(pctx.Freq))
}

func (a *pythonAggregator) Aggregate(rec any) {
	so, ok := rec.(profiler.SampleOutput)
	if !ok {
		log.P().Warnf("invalid record type %T, expected profiler.SampleOutput", rec)

		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.shouldKeepSampleOutput {
		a.sampleOutput = append(a.sampleOutput, profiler.SampleOutput{
			PID:    so.PID,
			Output: so.Output,
		})
	}

	if a.formatter == nil {
		return
	}

	lines := strings.Split(so.Output, "\n")
	for _, line := range lines {
		stack, count, ok := parseCollapsedLine(line)
		if !ok {
			continue
		}

		frames := strings.Split(stack, ";")
		if err := a.formatter.Add(&output.Sample{Frames: frames, Count: count}); err != nil {
			log.P().Warnf("formatter add sample: %v", err)
		}
	}
}

func (a *pythonAggregator) Snapshot(pctx *pcontext.ProfilerContext) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !pctx.OutputFormat.IsUpload() {
		if a.formatter != nil {
			a.formatter.Reset()
		}

		return nil, nil
	}

	if a.formatter != nil && a.formatter.IsEmpty() {
		return nil, nil
	}

	return a.snapshotPprof(pctx)
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

func (a *pythonAggregator) snapshotPprof(pctx *pcontext.ProfilerContext) (any, error) {
	if pctx.IsOneShotAgg {
		return a.snapshotPprofOneShot(pctx)
	}

	return a.snapshotPprofStreaming(pctx)
}

func (a *pythonAggregator) snapshotPprofOneShot(pctx *pcontext.ProfilerContext) (any, error) {
	var folded bytes.Buffer
	negatives := 0
	var negativeSamples []string

	rf := a.formatter.(*raw.Formatter)
	for stack, count := range rf.Counts() {
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
		log.P().Warnf("%s one-shot: dropped negatives=%d sample=%s", a.profilerName, negatives, strings.Join(negativeSamples, " | "))
	}

	if folded.Len() == 0 {
		log.P().Warnf("%s one-shot: no non-negative samples after filtering", a.profilerName)

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
		&profiler.ParseInput{
			StartTime:    time.Now(),
			ProfileType:  a.profileType,
			ProfilerName: a.profilerName,
			Data:         pprofFolded,
			Opt:          opt,
			PID:          pctx.PID,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to parse raw data: %w", err)
	}

	return pprofData, nil
}

func (a *pythonAggregator) snapshotPprofStreaming(pctx *pcontext.ProfilerContext) (any, error) {
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
		&profiler.ParseInput{
			StartTime:    time.Now(),
			ProfileType:  a.profileType,
			ProfilerName: a.profilerName,
			Data:         pprofFolded,
			Opt:          opt,
			PID:          pctx.PID,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to parse raw data: %w", err)
	}

	return pprofData, nil
}
