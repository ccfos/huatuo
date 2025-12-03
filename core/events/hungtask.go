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
	"sync/atomic"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/storage"
	"huatuo-bamai/internal/utils/bytesutil"
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
	data            []*metric.Data
	backoff         *backoff.Backoff
	nextAllowedTime time.Time
}

func init() {
	// OS such as Fedora-42 may disable this feature.
	sysctl := "/proc/sys/kernel/hung_task_timeout_secs"
	if _, err := os.Stat(sysctl); err != nil {
		return
	}

	tracing.RegisterEventTracing("hungtask", newHungTask)
}

func newHungTask() (*tracing.EventTracingAttr, error) {
	bo := backoff.NewWithoutJitter(3*time.Hour, 10*time.Minute)
	bo.SetDecay(1 * time.Hour)

	return &tracing.EventTracingAttr{
		TracingData: &hungTaskTracing{
			data: []*metric.Data{
				metric.NewCounterData("total", 0, "hungtask counter", nil),
			},
			backoff: bo,
		},
		Internal: 10,
		Flag:     tracing.FlagMetric | tracing.FlagTracing,
	}, nil
}

var hungtaskCounter int64

func (c *hungTaskTracing) Update() ([]*metric.Data, error) {
	c.data[0].Value = float64(atomic.LoadInt64(&hungtaskCounter))
	return c.data, nil
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

			atomic.AddInt64(&hungtaskCounter, 1)

			now := time.Now()
			if now.Before(c.nextAllowedTime) {
				continue
			}

			c.nextAllowedTime = now.Add(c.backoff.Duration())

			cpusBT, err := kmsgutil.GetAllCPUsBT()
			if err != nil {
				cpusBT = err.Error()
			}

			blockedProcessesBT, err := kmsgutil.GetBlockedProcessesBT()
			if err != nil {
				blockedProcessesBT = err.Error()
			}

			storage.Save("hungtask", "", time.Now(), &HungTaskTracerData{
				Pid:                   data.Pid,
				Comm:                  bytesutil.ToString(data.Comm[:]),
				CPUsStack:             cpusBT,
				BlockedProcessesStack: blockedProcessesBT,
			})
		}
	}
}
