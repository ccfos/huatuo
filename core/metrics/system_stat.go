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

// ref: https://github.com/flashcatcloud/categraf/blob/main/inputs/kernel/kernel.go
// ref: https://github.com/influxdata/telegraf/blob/master/plugins/inputs/kernel/kernel.go

import (
	"fmt"

	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

type systemStat struct {
	procFS procfs.FS
}

func init() {
	tracing.RegisterEventTracing("system_stat", newSystemStat)
}

func newSystemStat() (*tracing.EventTracingAttr, error) {
	procFS, err := procfs.NewDefaultFS()
	if err != nil {
		return nil, fmt.Errorf("system_stat: init procfs: %w", err)
	}

	return &tracing.EventTracingAttr{
		TracingData: &systemStat{procFS: procFS},
		Flag:        tracing.FlagMetric,
	}, nil
}

func (c *systemStat) Update() ([]*metric.Data, error) {
	stat, err := c.procFS.Stat()
	if err != nil {
		return nil, err
	}

	return []*metric.Data{
		metric.NewCounterData("context_switches_total", float64(stat.ContextSwitches),
			"Total number of context switches (from /proc/stat ctxt).", nil),
		metric.NewCounterData("interrupts_total", float64(stat.IRQTotal),
			"Total number of interrupts handled (from /proc/stat intr).", nil),
		metric.NewCounterData("processes_forked_total", float64(stat.ProcessCreated),
			"Total number of processes created by fork since boot (from /proc/stat processes).", nil),
		metric.NewGaugeData("procs_running", float64(stat.ProcessesRunning),
			"Processes currently in runnable state (from /proc/stat procs_running).", nil),
		metric.NewGaugeData("procs_blocked", float64(stat.ProcessesBlocked),
			"Processes currently blocked waiting for I/O (from /proc/stat procs_blocked).", nil),
		metric.NewGaugeData("boot_time_seconds", float64(stat.BootTime),
			"Boot time in seconds since the Epoch (from /proc/stat btime).", nil),
	}, nil
}
