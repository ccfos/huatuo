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

package aggregator

import (
	"context"
	"fmt"
	"time"

	"huatuo-bamai/core/autotracing"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler"
	profctx "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/pkg/tracing"
)

const profilerTracerName = "profiler"

func (p *Pipeline) saveProfilingDocument(_ context.Context, data any) error {
	if p.pctx.ToolstreamClient == nil {
		return fmt.Errorf("toolstream client not initialized")
	}

	flameData, ok := data.(*profiler.ProfileData)
	if !ok {
		return fmt.Errorf("invalid pprof data for uploading: %T", data)
	}

	tracerData := &profctx.TracerData{
		MetricData: newMetrics(int(p.overflowCount.Load())),
		FlameData:  flameData,
	}

	ev := &autotracing.ProfilerEvent{
		TracerID:      p.tracerID,
		ContainerID:   p.pctx.ContainerID,
		TracerName:    profilerTracerName,
		TracerRunType: tracing.TracerRunTypeTask,
		TracerTime:    time.Now().Format("2006-01-02 15:04:05.000 -0700"),
		TracerData:    tracerData,
	}

	if err := p.pctx.ToolstreamClient.Send(ev); err != nil {
		log.WithField("tracer_id", p.tracerID).Errorf("failed to send profiling event: %v", err)
		return err
	}

	log.WithField("tracer_id", p.tracerID).Infof("profiling event sent via toolstream")

	return nil
}
