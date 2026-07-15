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
)

func (p *Pipeline) saveProfilingDocument(_ context.Context, data any) error {
	if p.pctx.ToolstreamClient == nil {
		return fmt.Errorf("toolstream client not initialized")
	}

	if len(p.pctx.CpuIdleMetaData) != 0 && len(p.pctx.CpuSysMetaData) != 0 {
		return fmt.Errorf("cpu idle and sys metadata are both set; only one is allowed")
	}

	var autoMeta any

	if len(p.pctx.CpuIdleMetaData) != 0 {
		cpuIdleMeta, err := profctx.MapToStructByJSON[autotracing.CPUIdleMetaData](p.pctx.CpuIdleMetaData)
		if err != nil {
			return fmt.Errorf("failed to map CPU idle metadata: %w", err)
		}

		autoMeta = cpuIdleMeta
	}

	if len(p.pctx.CpuSysMetaData) != 0 {
		cpuSysMeta, err := profctx.MapToStructByJSON[autotracing.CpuSysMetaData](p.pctx.CpuSysMetaData)
		if err != nil {
			return fmt.Errorf("failed to map CPU sys metadata: %w", err)
		}

		autoMeta = cpuSysMeta
	}

	flameData, ok := data.(*profiler.ProfileData)
	if !ok {
		return fmt.Errorf("invalid pprof data for uploading: %T", data)
	}
	if err := profiler.ApplyLabels(flameData, p.pctx.Labels); err != nil {
		return fmt.Errorf("inject profiling labels: %w", err)
	}

	tracerData := &profctx.TracerData{
		MetricData: newMetrics(int(p.overflowCount.Load())),
		FlameData:  flameData,
		MetaData:   autoMeta,
	}

	tracerID := p.pctx.MetaData["tracer_id"]
	if tracerID == "" {
		tracerID = p.tracerID
	}

	ev := &autotracing.ProfilerEvent{
		TracerID:      tracerID,
		ContainerID:   p.pctx.ContainerID,
		TracerName:    p.pctx.MetaData["tracer_name"],
		TracerRunType: p.pctx.MetaData["tracer_type"],
		TracerTime:    time.Now().Format("2006-01-02 15:04:05.000 -0700"),
		TracerData:    tracerData,
	}

	if err := p.pctx.ToolstreamClient.Send(ev); err != nil {
		log.WithField("tracer_id", tracerID).Errorf("failed to send profiling event: %v", err)
		return err
	}

	log.WithField("tracer_id", tracerID).Infof("profiling event sent via toolstream")

	return nil
}
