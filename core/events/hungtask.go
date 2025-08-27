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

package events

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/storage"
	"huatuo-bamai/internal/utils/kmsgutil"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"

	"github.com/cloudflare/backoff"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/hungtask.c -o $BPF_DIR/hungtask.o

type hungTaskPerfEventData struct {
	Pid  int32
	Comm [bpf.TaskCommLen]byte
}

// HungTaskTracerData is the full data structure.
type HungTaskTracerData struct {
	Pid                   int32  `json:"pid"`
	Comm                  string `json:"comm"`
	CPUsStack             string `json:"cpus_stack"`
	BlockedProcessesStack string `json:"blocked_processes_stack"`
}

type hungTaskTracing struct {
	metric                 []*metric.Data
	bo                     *backoff.Backoff
	nextCaptureAllowedTime time.Time
}

func init() {
	// Some OS distributions such as Fedora-42 may disable this feature.
	hungTaskSysctl := "/proc/sys/kernel/hung_task_timeout_secs"
	if _, err := os.Stat(hungTaskSysctl); err != nil {
		return
	}

	tracing.RegisterEventTracing("hungtask", newHungTask)
}

func newHungTask() (*tracing.EventTracingAttr, error) {
	bo := backoff.NewWithoutJitter(3*time.Hour, 10*time.Minute)
	bo.SetDecay(1 * time.Hour)
	return &tracing.EventTracingAttr{
		TracingData: &hungTaskTracing{
			metric: []*metric.Data{
				metric.NewGaugeData("counter", 0, "hungtask counter", nil),
			},
			bo: bo,
		},
		Internal: 10,
		Flag:     tracing.FlagMetric | tracing.FlagTracing,
	}, nil
}

var hungtaskCounter float64

func (c *hungTaskTracing) Update() ([]*metric.Data, error) {
	c.metric[0].Value = hungtaskCounter
	return c.metric, nil
}

func (c *hungTaskTracing) Start(ctx context.Context) error {
	b, err := bpf.LoadBpf(bpf.ThisBpfOBJ(), nil)
	if err != nil {
		return err
	}
	defer b.Close()

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	reader, err := b.AttachAndEventPipe(childCtx, "hungtask_perf_events", 8192)
	if err != nil {
		return err
	}
	defer reader.Close()

	b.WaitDetachByBreaker(childCtx, cancel)

	for {
		select {
		case <-childCtx.Done():
			return nil
		default:
			var data hungTaskPerfEventData
			if err := reader.ReadInto(&data); err != nil {
				return fmt.Errorf("hungtask ReadFromPerfEvent: %w", err)
			}

			now := time.Now()
			if now.Before(c.nextCaptureAllowedTime) {
				hungtaskCounter++
				continue
			}

			c.nextCaptureAllowedTime = now.Add(c.bo.Duration())

			cpusBT, err := kmsgutil.GetAllCPUsBT()
			if err != nil {
				cpusBT = err.Error()
			}

			blockedProcessesBT, err := kmsgutil.GetBlockedProcessesBT()
			if err != nil {
				blockedProcessesBT = err.Error()
			}

			hungtaskCounter++

			storage.Save("hungtask", "", time.Now(), &HungTaskTracerData{
				Pid:                   data.Pid,
				Comm:                  strings.TrimRight(string(data.Comm[:]), "\x00"),
				CPUsStack:             cpusBT,
				BlockedProcessesStack: blockedProcessesBT,
			})
		}
	}
}
