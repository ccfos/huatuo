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

package events

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/internal/utils/bytesutil"
	"github.com/ccfos/huatuo/internal/utils/kmsgutil"
	"github.com/ccfos/huatuo/pkg/metric"

	"github.com/cloudflare/backoff"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/softlockup.c -o $BPF_DIR/softlockup.o

// TracerData is the full data structure.
type SoftLockupTracerData struct {
	CPU       uint32 `json:"cpu"`
	PID       uint32 `json:"pid"`
	Comm      string `json:"comm"`
	CPUsStack string `json:"cpus_stack"`
}

type softLockupTracing struct {
	backoff         *backoff.Backoff
	nextAllowedTime time.Time
}

func init() {
	tracing.RegisterEventTracing("softlockup", newSoftLockup)
}

func newSoftLockup() (*tracing.EventTracingAttr, error) {
	bo := backoff.NewWithoutJitter(3*time.Hour, 10*time.Minute)
	bo.SetDecay(1 * time.Hour)

	return &tracing.EventTracingAttr{
		TracingData: &softLockupTracing{
			backoff: bo,
		},
		Interval: 10,
		Flag:     tracing.FlagTracing | tracing.FlagMetric,
	}, nil
}

var softlockupCounter int64

// Update returns freshly built data: doCollect reads Data.Value outside the
// collector mutex, so cached Data would race with the next scrape's Update.
func (c *softLockupTracing) Update() ([]*metric.Data, error) {
	return []*metric.Data{
		metric.NewCounterData("total", float64(atomic.LoadInt64(&softlockupCounter)), "softlockup counter", nil),
	}, nil
}

func (c *softLockupTracing) Start(ctx context.Context) error {
	b, err := bpf.LoadBPF(bpf.ThisBpfOBJ(), nil)
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

	b.DetachOnContextDone(childCtx, cancel)

	for {
		select {
		case <-childCtx.Done():
			return nil
		default:
			var data abi.SoftlockupEvent
			if err := reader.ReadInto(&data); err != nil {
				if errors.Is(err, bpf.ErrPerfEventSamplesLost) {
					log.WithError(err).Warn("lost BPF perf event samples")
					continue
				}
				return fmt.Errorf("ReadFromPerfEvent fail: %w", err)
			}

			atomic.AddInt64(&softlockupCounter, 1)

			now := time.Now()
			if now.Before(c.nextAllowedTime) {
				continue
			}

			c.nextAllowedTime = now.Add(c.backoff.Duration())

			bt, err := kmsgutil.GetAllCPUsBT()
			if err != nil {
				bt = err.Error()
			}

			if err := tracing.Save(&tracing.WriteRequest{
				TracerName:        "softlockup",
				ObservedTimestamp: time.Now().UTC(),
				TracerData: &SoftLockupTracerData{
					CPU:       data.CPU,
					PID:       data.TGID,
					Comm:      bytesutil.ToStr(data.Comm[:]),
					CPUsStack: bt,
				},
			}); err != nil {
				log.Warnf("failed to save tracing data: %v", err)
			}
		}
	}
}
