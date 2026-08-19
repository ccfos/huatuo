// Copyright 2026 The HuaTuo Authors
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
	"sort"
	"strings"
	"sync"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler"
	"huatuo-bamai/internal/profiler/aggregator"
	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/output"
)

var _ aggregator.Aggregator = (*pythonMemoryAggregator)(nil)

type pythonMemoryAggregator struct {
	mu sync.Mutex

	formatter   output.Formatter
	counts      map[string]int64
	startedAt   time.Time
	snapshotCtx context.Context
}

func newPythonMemoryAggregator(pctx *pcontext.ProfilerContext) (*pythonMemoryAggregator, error) {
	// Memray records are signed deltas. Export only after every accepted
	// allocation and free has been folded into the retained totals.
	pctx.IsOneShotAgg = true

	formatter, err := aggregator.NewFormatterForOutput(pctx)
	if err != nil {
		return nil, err
	}

	snapshotCtx := pctx.Ctx
	if snapshotCtx == nil {
		snapshotCtx = context.Background()
	} else {
		snapshotCtx = context.WithoutCancel(snapshotCtx)
	}

	return &pythonMemoryAggregator{
		formatter:   formatter,
		counts:      make(map[string]int64),
		startedAt:   time.Now(),
		snapshotCtx: snapshotCtx,
	}, nil
}

func (a *pythonMemoryAggregator) Aggregate(rec any) {
	sample, ok := rec.(profiler.SampleOutput)
	if !ok {
		log.Warnf("invalid record type %T, expected profiler.SampleOutput", rec)
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	for _, line := range strings.Split(sample.Output, "\n") {
		stack, count, ok := parseCollapsedLine(line)
		if !ok {
			continue
		}

		a.counts[stack] += count
		if a.counts[stack] == 0 {
			delete(a.counts, stack)
		}
	}
}

func (a *pythonMemoryAggregator) Snapshot(pctx *pcontext.ProfilerContext) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !pctx.OutputFormat.IsUpload() {
		return nil, nil
	}

	folded := a.positiveFoldedLocked()
	if folded == "" {
		return nil, nil
	}

	data, err := json.Marshal([]profiler.SampleOutput{{
		PID:    pctx.PID(),
		Output: folded,
	}})
	if err != nil {
		return nil, fmt.Errorf("marshal Memray samples: %w", err)
	}

	profile, err := profiler.ParseRawData(
		a.snapshotCtx,
		&profiler.ParseInput{
			StartTime:    a.startedAt,
			ProfileType:  profiler.ProfileTypeMemSample,
			ProfilerName: "python-mem",
			Data:         data,
			Opt:          &profiler.ParseOption{SampleRate: profiler.NoSampleRate},
			PID:          pctx.PID(),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("parse Memray samples: %w", err)
	}

	return profile, nil
}

func (a *pythonMemoryAggregator) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.formatter != nil {
		a.formatter.Reset()
	}
	a.counts = make(map[string]int64)
}

func (a *pythonMemoryAggregator) OutputFormatter() output.Formatter {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.formatter == nil {
		return nil
	}

	a.formatter.Reset()
	for stack, count := range a.counts {
		if count <= 0 {
			continue
		}
		if err := a.formatter.Add(&output.Sample{
			Frames: strings.Split(stack, ";"),
			Count:  count,
		}); err != nil {
			log.Warnf("formatter add Memray sample: %v", err)
		}
	}

	return a.formatter
}

func (a *pythonMemoryAggregator) positiveFoldedLocked() string {
	keys := make([]string, 0, len(a.counts))
	for stack, count := range a.counts {
		if count > 0 {
			keys = append(keys, stack)
		}
	}
	sort.Strings(keys)

	var folded strings.Builder
	for _, stack := range keys {
		fmt.Fprintf(&folded, "%s %d\n", stack, a.counts[stack])
	}
	return folded.String()
}
