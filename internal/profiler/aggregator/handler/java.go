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
	context "huatuo-bamai/internal/profiler/context"
)

type javaAggregator struct {
	mu sync.Mutex
	*aggregator.Aggregator

	countMap     map[string]int
	sampleOutput []profiler.SampleOutput
}

// NewJavaAggregator create agregator
func NewJavaAggregator(pctx *context.ProfilerContext) *javaAggregator {
	aggr := &javaAggregator{}
	aggr.Aggregator = aggregator.NewAggregator(pctx, aggr.RecordProcessor, aggr.AggregatedExporter)
	aggr.countMap = make(map[string]int)
	aggr.sampleOutput = make([]profiler.SampleOutput, 0)
	return aggr
}

func (a *javaAggregator) RecordProcessor(rec any) {
	so, ok := rec.(profiler.SampleOutput)
	if !ok {
		log.Infof("invalid record")
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	a.sampleOutput = append(a.sampleOutput, profiler.SampleOutput{
		PID:    so.PID,
		Output: so.Output,
	})

	pid := so.PID
	output := so.Output

	// Each line: stack count
	lines := strings.Split(output, "\n")

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

		count, err := strconv.Atoi(countStr)
		if err != nil {
			continue
		}

		// Join with semicolon ;
		finalStack := fmt.Sprintf("process %d;%s", pid, stack)
		a.countMap[finalStack] += count
	}
}

func (a *javaAggregator) AggregatedExporter(pctx *context.ProfilerContext) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.countMap) == 0 {
		log.Infof("There is no data in aggregate queue")
		return nil, nil
	}

	// Build final result, one line per stack
	var foldedArr [][]byte
	for stack, count := range a.countMap {
		line := fmt.Sprintf("%s %d", stack, count)
		foldedArr = append(foldedArr, []byte(line))
	}

	folded := bytes.Join(foldedArr, []byte("\n"))

	switch pctx.OutputFormat {
	case "pprof", "es":
		if len(a.sampleOutput) == 0 {
			return nil, nil
		}

		pprofFolded, err := json.MarshalIndent(a.sampleOutput, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("failed to marshal sample output: %w", err)
		}

		var opt *profiler.ParseOption
		var sampleType string
		var prName string

		switch pctx.Type {
		case "cpu":
			opt = &profiler.ParseOption{SampleRate: int64(pctx.Freq)}
			sampleType = profiler.ProfileTypeCpuSample
			prName = "java-cpu"
		case "mem":
			opt = &profiler.ParseOption{SampleRate: profiler.NoSampleRate}
			sampleType = profiler.ProfileTypeMemSample
			prName = "java-mem"
		case "lock":
			opt = &profiler.ParseOption{SampleRate: profiler.NoSampleRate}
			sampleType = profiler.ProfileTypeLockTimeSample
			outputType := pctx.ExtraFlags["mode"]
			if outputType == "count" {
				sampleType = profiler.ProfileTypeLockCountSample
			}
			prName = "java-lock"
		default:
			return nil, fmt.Errorf("unsupported profile type: %s", pctx.Type)
		}

		pprofData, err := profiler.ParseCollapsedData(
			pctx.Ctx,
			time.Now(),
			sampleType,
			prName,
			pprofFolded,
			opt,
			pctx.PID,
		)
		if err != nil {
			return nil, err
		}

		// reset clears all aggregated data after each export window
		a.reset()

		return pprofData, nil
	case "raw", "flamegraph", "svg":
		return folded, nil
	default:
		return folded, nil
	}
}

// reset clears all aggregated Java profiling data; must be called with a.mu held
func (a *javaAggregator) reset() {
	a.countMap = make(map[string]int)
	a.sampleOutput = make([]profiler.SampleOutput, 0)
}
