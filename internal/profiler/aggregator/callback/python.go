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

package callback

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
)

type pythonAggregator struct {
	mu sync.Mutex
	*aggregator.Aggregator

	countMap         map[string]int64
	sampleOutput     []profiler.SampleOutput
	keepSampleOutput bool
	profilerName     string // "python-mem" or "python-cpu"
	profileType      string // profiler.ProfileTypeMemSample or ProfileTypeCpuSample
	sampleRate       int64  // NoSampleRate for mem; pctx.Freq for cpu
}

func newPythonAggregator(pctx *pcontext.ProfilerContext, name, typ string, rate int64) *pythonAggregator {
	aggr := &pythonAggregator{
		profilerName: name,
		profileType:  typ,
		sampleRate:   rate,
	}
	aggr.Aggregator = aggregator.NewAggregator(pctx, aggr.RecordProcessor, aggr.AggregatedExporter)
	aggr.countMap = make(map[string]int64)
	aggr.sampleOutput = make([]profiler.SampleOutput, 0)
	aggr.keepSampleOutput = (pctx.OutputFormat == "pprof" || pctx.OutputFormat == "es") && !pctx.OneShotAgg
	return aggr
}

func NewPythonAggregator(pctx *pcontext.ProfilerContext) *pythonAggregator {
	return newPythonAggregator(pctx, "python-mem", profiler.ProfileTypeMemSample, profiler.NoSampleRate)
}

// NewPythonCPUAggregator creates an aggregator for py-spy CPU profiles.
func NewPythonCPUAggregator(pctx *pcontext.ProfilerContext) *pythonAggregator {
	return newPythonAggregator(pctx, "python-cpu", profiler.ProfileTypeCpuSample, int64(pctx.Freq))
}

func (a *pythonAggregator) RecordProcessor(rec any) {
	so, ok := rec.(profiler.SampleOutput)
	if !ok {
		log.Infof("invalid record")
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

		a.countMap[stack] += count
	}
}

func (a *pythonAggregator) AggregatedExporter(pctx *pcontext.ProfilerContext) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.countMap) == 0 {
		log.Infof("There is no data in %s aggregate queue", a.profilerName)
		return nil, nil
	}

	var foldedArr [][]byte
	var foldedArrFiltered [][]byte
	negatives := 0
	var negativeSamples []string
	for stack, count := range a.countMap {
		line := fmt.Sprintf("%s %d", stack, count)
		foldedArr = append(foldedArr, []byte(line))
		if pctx.OneShotAgg && (pctx.OutputFormat == "pprof" || pctx.OutputFormat == "es") {
			if count < 0 {
				negatives++
				if len(negativeSamples) < 5 {
					negativeSamples = append(negativeSamples, line)
				}
				continue
			}
			foldedArrFiltered = append(foldedArrFiltered, []byte(line))
		}
	}

	folded := bytes.Join(foldedArr, []byte("\n"))
	if pctx.OneShotAgg && (pctx.OutputFormat == "pprof" || pctx.OutputFormat == "es") {
		if negatives > 0 {
			log.P().Infof("[profiler] %s one-shot: dropped negatives=%d sample=%s", a.profilerName, negatives, strings.Join(negativeSamples, " | "))
		}
		folded = bytes.Join(foldedArrFiltered, []byte("\n"))
		if len(folded) == 0 {
			log.P().Infof("[profiler] %s one-shot: no non-negative samples after filtering", a.profilerName)
			return nil, nil
		}
	}
	// Emit per-interval deltas and let the base aggregator accumulate totals.
	// This avoids double-aggregation when alloc/free fall into different intervals.
	a.countMap = make(map[string]int64)

	switch pctx.OutputFormat {
	case "pprof", "es":
		if pctx.OneShotAgg {
			pprofFolded, err := json.MarshalIndent([]profiler.SampleOutput{
				{PID: pctx.PID, Output: string(folded)},
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
				return nil, err
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
			return nil, err
		}
		return pprofData, nil
	case "raw", "flamegraph", "svg":
		return folded, nil
	default:
		return folded, nil
	}
}
