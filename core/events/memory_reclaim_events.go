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
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/internal/utils/bytesutil"
)

type memoryReclaimTracing struct{}

// MemoryReclaimTracingData is the full data structure.
type MemoryReclaimTracingData struct {
	PID                  uint32 `json:"pid"`
	TID                  uint32 `json:"tid"`
	Comm                 string `json:"comm"`
	ReclaimDurationNS    uint64 `json:"reclaim_duration_ns"`
	ContainerAttribution string `json:"container_attribution,omitempty"`
}

func init() {
	tracing.RegisterEventTracing("memory_reclaim_events", newMemoryReclaim)
}

func newMemoryReclaim() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &memoryReclaimTracing{},
		Interval:    5,
		Flag:        tracing.FlagTracing,
	}, nil
}

const cssCacheTTL = 5 * time.Second

// Retry misses sooner than the TTL without rediscovering containers for every
// unresolved host event. The output scope must not change attribution refresh.
const reclaimCacheMissRetry = time.Second

type reclaimContainerCache struct {
	containers  map[uint64]*pod.Container
	refreshedAt time.Time
	attemptedAt time.Time
}

func (c *reclaimContainerCache) lookup(
	css uint64, now time.Time,
	refresh func() (map[uint64]*pod.Container, error),
) (*pod.Container, error) {
	expired := c.refreshedAt.IsZero() || now.Sub(c.refreshedAt) > cssCacheTTL
	container := c.containers[css]
	if !expired && (container != nil || now.Sub(c.attemptedAt) < reclaimCacheMissRetry) {
		return container, nil
	}
	c.attemptedAt = now
	containers, err := refresh()
	if err != nil {
		if expired {
			// Do not attribute through an expired cache after discovery fails.
			c.containers = nil
			c.refreshedAt = now
		}
		return nil, err
	}
	c.containers = containers
	c.refreshedAt = now
	return containers[css], nil
}

// Start detect work, load bpf and wait data form perfevent
//
//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/memory_reclaim_events.c -o $BPF_DIR/memory_reclaim_events.o
func (c *memoryReclaimTracing) Start(ctx context.Context) error {
	cfg := configSnapshot()
	b, err := bpf.LoadBPF(bpf.ThisBpfOBJ(), map[string]any{
		"reclaim_duration_threshold_ns": cfg.MemoryReclaim.BlockedThreshold,
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

	b.DetachOnContextDone(childCtx, cancel)

	var cache reclaimContainerCache

	refreshContainerCache := func() (map[uint64]*pod.Container, error) {
		containers, err := pod.Containers()
		if err != nil {
			return nil, err
		}
		return pod.BuildCssContainers(containers, subsystem.SubsystemCPU), nil
	}

	for {
		select {
		case <-childCtx.Done():
			return nil
		default:
			var data abi.MemoryReclaimEvent
			if err := reader.ReadInto(&data); err != nil {
				if errors.Is(err, bpf.ErrPerfEventSamplesLost) {
					log.WithError(err).Warn("lost BPF perf event samples")
					continue
				}
				return fmt.Errorf("ReadFromPerfEvent fail: %w", err)
			}

			container, err := cache.lookup(data.CPUCSSAddr, time.Now(), refreshContainerCache)
			if err != nil {
				log.Errorf("refresh container cache: %v", err)
			}
			containerID, attribution := reclaimEventTarget(container)

			// save storage
			tracingData := &MemoryReclaimTracingData{
				PID:                  data.TGID,
				TID:                  data.TID,
				Comm:                 bytesutil.ToStr(data.Comm[:]),
				ReclaimDurationNS:    data.ReclaimDurationNS,
				ContainerAttribution: attribution,
			}

			log.Infof("memory_reclaim saves storage: %+v", tracingData)
			if err := tracing.Save(&tracing.WriteRequest{
				TracerName:        "memory_reclaim",
				ContainerID:       containerID,
				ObservedTimestamp: time.Now().UTC(),
				TracerData:        tracingData,
			}); err != nil {
				log.Warnf("failed to save tracing data: %v", err)
			}
		}
	}
}

func reclaimEventTarget(container *pod.Container) (string, string) {
	if container != nil {
		return container.ID, ""
	}
	// A cache miss does not prove that the task is outside a container.
	return "", "unresolved"
}
