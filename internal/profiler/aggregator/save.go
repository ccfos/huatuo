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

	"huatuo-bamai/core/autotracing"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler"
	profctx "huatuo-bamai/internal/profiler/context"
)

func (p *Pipeline) saveProfilingDocument(ctx context.Context, data any) error {
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

	tracerData := &profctx.TracerData{
		MetricData: newMetrics(int(p.overflowCount.Load())),
		FlameData:  flameData,
		MetaData:   autoMeta,
	}

	doc := profiler.ProfilingDocumentMapper{}.CreateProfilingDocument(p.pctx.MetaData, p.pctx.ContainerID, p.pctx.ServerAddress)
	if doc == nil {
		return fmt.Errorf("failed to build profiler document")
	}

	doc.TracerData = tracerData

	if doc.TracerID == "" {
		doc.TracerID = p.tracerID
	}

	if p.pctx.DataSaver != nil {
		if err := p.pctx.DataSaver.Save(ctx, doc); err != nil {
			log.P().WithField("tracer_id", p.tracerID).Errorf("failed to save profiling metadata: %v", err)
		}
	}

	log.P().WithField("tracer_id", doc.TracerID).Infof("profiling document saved")

	return nil
}
