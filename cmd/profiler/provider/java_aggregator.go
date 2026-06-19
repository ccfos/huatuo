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

// Compile-time check: javaAggregator implements aggregator.Aggregator.
var _ aggregator.Aggregator = (*javaAggregator)(nil)

type javaAggregator struct {
	mu sync.Mutex

	formatter    output.Formatter
	sampleOutput []profiler.SampleOutput
}

func newJavaAggregator(pctx *pcontext.ProfilerContext) (*javaAggregator, error) {
	f, err := aggregator.NewFormatterForOutput(pctx)
	if err != nil {
		return nil, err
	}

	return &javaAggregator{
		formatter: f,
	}, nil
}

func (a *javaAggregator) Aggregate(rec any) {
	so, ok := rec.(profiler.SampleOutput)
	if !ok {
		log.P().Warnf("invalid record type %T, expected profiler.SampleOutput", rec)

		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	a.sampleOutput = append(a.sampleOutput, so)

	if a.formatter == nil {
		return
	}

	lines := strings.Split(so.Output, "\n")

	for _, line := range lines {
		stack, count, ok := parseCollapsedLine(line)
		if !ok {
			continue
		}

		frames := []string{fmt.Sprintf("process %d", so.PID), stack}
		if err := a.formatter.Add(&output.Sample{Frames: frames, Count: count}); err != nil {
			log.P().Warnf("formatter add sample: %v", err)
		}
	}
}

func (a *javaAggregator) Snapshot(pctx *pcontext.ProfilerContext) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !pctx.OutputFormat.IsUpload() {
		return nil, nil
	}

	return a.snapshotPprof(pctx)
}

func (a *javaAggregator) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.formatter != nil {
		a.formatter.Reset()
	}

	a.sampleOutput = nil
}

func (a *javaAggregator) OutputFormatter() output.Formatter {
	return a.formatter
}

func (a *javaAggregator) snapshotPprof(pctx *pcontext.ProfilerContext) (any, error) {
	if len(a.sampleOutput) == 0 {
		return nil, nil
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
		time.Now(),
		sampleType,
		prName,
		pprofFolded,
		opt,
		pctx.PID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to parse collapsed data: %w", err)
	}

	return pprofData, nil
}

func javaParseOptions(pctx *pcontext.ProfilerContext) (*profiler.ParseOption, string, string, error) {
	opt, sampleType, err := profileTypeOptions(pctx)
	if err != nil {
		return nil, "", "", err
	}

	prName := "java-" + pctx.Type
	return opt, sampleType, prName, nil
}
