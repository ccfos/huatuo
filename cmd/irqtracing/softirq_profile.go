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

package main

import (
	"fmt"
	"strconv"
	"time"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"

	"github.com/ccfos/huatuo/internal/profiler"
	pcontext "github.com/ccfos/huatuo/internal/profiler/context"
	"github.com/ccfos/huatuo/pkg/types"
)

type softIRQBatch struct {
	profile   *profiler.ProfileData
	duration  time.Duration
	dropped   uint64
	targetCPU int32
}

type irqAggregator struct{ batch *softIRQBatch }

func (a *irqAggregator) Aggregate(record any) { a.batch = record.(*softIRQBatch) }

func (a *irqAggregator) Snapshot(pctx *pcontext.ProfilerContext) (*types.ProfilingWindow, error) {
	if a.batch == nil {
		return nil, fmt.Errorf("IRQ collection completed without a batch")
	}
	window, err := profiler.NewWindow(a.batch.profile, pctx.ContainerID, a.batch.duration)
	if err != nil {
		return nil, err
	}
	if a.batch.dropped != 0 {
		window.DroppedSamples = map[string]uint64{types.DropReasonCollection: a.batch.dropped}
	}
	// Target CPU is a sample dimension, not a second window timestamp or IRQ envelope.
	if a.batch.targetCPU >= 0 {
		key := int64(len(window.Profile.StringTable))
		window.Profile.StringTable = append(window.Profile.StringTable, "cpu", strconv.Itoa(int(a.batch.targetCPU)))
		for _, sample := range window.Profile.Sample {
			sample.Label = append(sample.Label, &profilev1.Label{Key: key, Str: key + 1})
		}
	}
	return window, nil
}

func (a *irqAggregator) Reset() { a.batch = nil }
