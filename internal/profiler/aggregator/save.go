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
	"sort"
	"strconv"
	"strings"
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
	setProfileCollectionLabels(flameData, p.pctx)

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

func setProfileCollectionLabels(
	data *profiler.ProfileData,
	pctx *profctx.ProfilerContext,
) {
	if data.Labels == nil {
		data.Labels = make(map[string]string)
	}
	for _, name := range profiler.CollectionDimensionLabelNames() {
		delete(data.Labels, name)
	}
	for name, value := range profileCollectionLabels(pctx) {
		data.Labels[name] = value
	}
}

func profileCollectionLabels(pctx *profctx.ProfilerContext) map[string]string {
	labels := map[string]string{
		profiler.LabelProfilingScope: profileCollectionScope(pctx),
	}
	if cpu := formatProfileLabelIDs(pctx.CPUIDs); cpu != "" {
		labels[profiler.LabelCPU] = cpu
	}
	if pctx.ThreadGroup {
		if tgid := formatProfileLabelIDs(pctx.PIDs); tgid != "" {
			labels[profiler.LabelTGID] = tgid
		}
	} else if pid := formatProfileLabelIDs(pctx.PIDs); pid != "" {
		labels[profiler.LabelPID] = pid
	}
	if pctx.ContainerID != "" {
		labels[profiler.LabelContainerID] = pctx.ContainerID
	}
	return labels
}

func profileCollectionScope(pctx *profctx.ProfilerContext) string {
	switch {
	case pctx.ContainerID != "":
		return "container"
	case pctx.ThreadGroup:
		return "thread_group"
	case len(pctx.PIDs) != 0:
		return "pid"
	case len(pctx.CPUIDs) != 0:
		return "cpu"
	default:
		return "host"
	}
}

func formatProfileLabelIDs(ids []int) string {
	if len(ids) == 0 {
		return ""
	}

	sorted := append([]int(nil), ids...)
	sort.Ints(sorted)
	values := make([]string, 0, len(sorted))
	for index, id := range sorted {
		if index > 0 && id == sorted[index-1] {
			continue
		}
		values = append(values, strconv.Itoa(id))
	}
	return strings.Join(values, ",")
}
