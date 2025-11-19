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
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/storage"
	"huatuo-bamai/internal/utils/bytesutil"
	"huatuo-bamai/pkg/tracing"
)

type txqueueTracingData struct {
	QueueIndex uint32 `json:"queue_index"`
	Name       string `json:"device_name"`
	Driver     string `json:"driver_name"`
}

const deviceNameLen = 16

type txqueuePerfEvent struct {
	QueueIndex uint32
	Name       [deviceNameLen]byte
	Driver     [deviceNameLen]byte
}

type txqueueTimeout struct{}

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/netdev_txqueue_timeout.c -o $BPF_DIR/netdev_txqueue_timeout.o

func init() {
	tracing.RegisterEventTracing("netdev_txqueue_timeout", newTxqueueTimeout)
}

func newTxqueueTimeout() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &txqueueTimeout{},
		Internal:    10,
		Flag:        tracing.FlagTracing,
	}, nil
}

func (c *txqueueTimeout) Start(ctx context.Context) error {
	b, err := bpf.LoadBpf(bpf.ThisBpfOBJ(), nil)
	if err != nil {
		return err
	}
	defer b.Close()

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	reader, err := b.AttachAndEventPipe(childCtx, "perf_events", 8192)
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
			var event txqueuePerfEvent

			if err := reader.ReadInto(&event); err != nil {
				return err
			}

			data := txqueueTracingData{
				QueueIndex: event.QueueIndex,
				Name:       bytesutil.CString(event.Name[:]),
				Driver:     bytesutil.CString(event.Driver[:]),
			}

			storage.Save("netdev_txqueue_timeout", "", time.Now(), data)
		}
	}
}
