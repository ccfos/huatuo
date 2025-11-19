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
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/conf"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/storage"
	"huatuo-bamai/pkg/tracing"
)

type memoryReclaimTracing struct{}

type memoryReclaimPerfEvent struct {
	Comm      [bpf.TaskCommLen]byte
	Deltatime uint64
	CSS       uint64
	Pid       uint64
}

// MemoryReclaimTracingData is the full data structure.
type MemoryReclaimTracingData struct {
	Pid       uint64 `json:"pid"`
	Comm      string `json:"comm"`
	Deltatime uint64 `json:"deltatime"`
}

func init() {
	tracing.RegisterEventTracing("memory_reclaim_events", newMemoryReclaim)
}

func newMemoryReclaim() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &memoryReclaimTracing{},
		Internal:    5,
		Flag:        tracing.FlagTracing,
	}, nil
}

const cssCacheTTL = 5 * time.Second

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/memory_reclaim_events.c -o $BPF_DIR/memory_reclaim_events.o

// Start detect work, load bpf and wait data form perfevent
func (c *memoryReclaimTracing) Start(ctx context.Context) error {
	b, err := bpf.LoadBpf(bpf.ThisBpfOBJ(), map[string]any{
		"deltath": conf.Get().EventTracing.MemoryReclaim.BlockedThreshold,
	})
	if err != nil {
		return err
	}
	defer b.Close()

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	reader, err := b.AttachAndEventPipe(childCtx, "reclaim_perf_events", 8192)
	if err != nil {
		return err
	}
	defer reader.Close()

	b.WaitDetachByBreaker(childCtx, cancel)

	var (
		cssToContainer map[uint64]*pod.Container
		cacheTime      time.Time
	)

	refreshContainerCache := func() error {
		containers, err := pod.GetAllContainers()
		if err != nil {
			return err
		}
		cssToContainer = pod.BuildCSSToContainer(containers, "cpu")
		cacheTime = time.Now()
		return nil
	}

	for {
		select {
		case <-childCtx.Done():
			return nil
		default:
			var data memoryReclaimPerfEvent
			if err := reader.ReadInto(&data); err != nil {
				return fmt.Errorf("ReadFromPerfEvent fail: %w", err)
			}

			if cssToContainer == nil || time.Since(cacheTime) > cssCacheTTL {
				if err := refreshContainerCache(); err != nil {
					log.Errorf("refresh container cache: %v", err)
					continue
				}
			}

			container := cssToContainer[data.CSS]
			if container == nil {
				if err := refreshContainerCache(); err != nil {
					log.Errorf("refresh container cache: %v", err)
					continue
				}
				container = cssToContainer[data.CSS]
				if container == nil {
					// We only care about the container and nothing else.
					// Though it may be unfair, that's just how life is.
					//
					// -- Tonghao Zhang, tonghao@bamaicloud.com
					continue
				}
			}

			// save storage
			tracingData := &MemoryReclaimTracingData{
				Pid:       data.Pid,
				Comm:      strings.Trim(string(data.Comm[:]), "\x00"),
				Deltatime: data.Deltatime,
			}

			log.Infof("memory_reclaim saves storage: %+v", tracingData)
			storage.Save("memory_reclaim", container.ID, time.Now(), tracingData)
		}
	}
}
