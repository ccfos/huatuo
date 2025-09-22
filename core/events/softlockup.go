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
	"strings"
	"sync/atomic"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/storage"
	"huatuo-bamai/internal/utils/kmsgutil"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/softlockup.c -o $BPF_DIR/softlockup.o

type softLockupPerfEventData struct {
	CPU  int32
	Pid  int32
	Comm [16]byte
}

// TracerData is the full data structure.
type SoftLockupTracerData struct {
	CPU       int32  `json:"cpu"`
	Pid       int32  `json:"pid"`
	Comm      string `json:"comm"`
	CPUsStack string `json:"cpus_stack"`
}

type softLockupTracing struct {
	data []*metric.Data
}

func init() {
	tracing.RegisterEventTracing("softlockup", newSoftLockup)
}

func newSoftLockup() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &softLockupTracing{
			data: []*metric.Data{
				metric.NewGaugeData("counter", 0, "softlockup counter", nil),
			},
		},
		Internal: 10,
		Flag:     tracing.FlagTracing | tracing.FlagMetric,
	}, nil
}

var softlockupCounter int64

func (c *softLockupTracing) Update() ([]*metric.Data, error) {
	c.data[0].Value = float64(atomic.LoadInt64(&softlockupCounter))
	return c.data, nil
}

func (c *softLockupTracing) Start(ctx context.Context) error {
	b, err := bpf.LoadBpf(bpf.ThisBpfOBJ(), nil)
	if err != nil {
		return err
	}
	defer b.Close()

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	reader, err := b.AttachAndEventPipe(childCtx, "softlockup_perf_events", 8192)
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
			var data softLockupPerfEventData
			if err := reader.ReadInto(&data); err != nil {
				return fmt.Errorf("ReadFromPerfEvent fail: %w", err)
			}

			bt, err := kmsgutil.GetAllCPUsBT()
			if err != nil {
				bt = err.Error()
			}

			atomic.AddInt64(&softlockupCounter, 1)

			storage.Save("softlockup", "", time.Now(), &SoftLockupTracerData{
				CPU:       data.CPU,
				Pid:       data.Pid,
				Comm:      strings.TrimRight(string(data.Comm[:]), "\x00"),
				CPUsStack: bt,
			})
		}
	}
}
