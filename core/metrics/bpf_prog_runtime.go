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

package collector

import (
	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/utils/cpuutil"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
	"huatuo-bamai/pkg/types"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/bpf_prog_runtime.c -o $BPF_DIR/bpf_prog_runtime.o

type bpfProgRuntimeCollector struct{}

var readProgRuntimeData = bpf.ReadBPFProgRuntimeData

func init() {
	tracing.RegisterEventTracing(bpf.ProgRuntimeProfilerName, newBPFProgRuntimeCollector)
}

func newBPFProgRuntimeCollector() (*tracing.EventTracingAttr, error) {
	cfg := configSnapshot()
	if !cfg.BPFProgRuntime.Enabled {
		return nil, types.ErrNotSupported
	}

	return &tracing.EventTracingAttr{
		TracingData: &bpfProgRuntimeCollector{},
		Flag:        tracing.FlagMetric,
	}, nil
}

func (c *bpfProgRuntimeCollector) Update() ([]*metric.Data, error) {
	metrics := []*metric.Data{}
	if onlineCPUs, err := cpuutil.ParseOnlineCores(cpuutil.SystemCPUOnlinePath); err == nil {
		metrics = append(metrics, metric.NewGaugeData(
			"online_cores",
			float64(onlineCPUs),
			"number of online logical cpus on the host.",
			nil,
		))
	}

	for _, profile := range readProgRuntimeData() {
		labels := map[string]string{"program": profile.ProgramName}
		up := float64(0)
		if profile.Up {
			up = 1
		}
		metrics = append(metrics,
			metric.NewGaugeData(
				"up",
				up,
				"whether all current bpf program instances are profiled.",
				labels,
			),
			metric.NewCounterData(
				"attach_failures_total",
				float64(profile.AttachFailures),
				"target bpf program profiler attach failures.",
				labels,
			),
			metric.NewCounterData(
				"runs_total",
				float64(profile.RunCount),
				"cumulative target bpf program executions.",
				labels,
			),
			metric.NewCounterData(
				"runtime_seconds_total",
				float64(profile.RunTimeNS)/1e9,
				"cumulative target bpf program runtime in seconds, excluding profiler overhead.",
				labels,
			),
		)
	}
	return metrics, nil
}
