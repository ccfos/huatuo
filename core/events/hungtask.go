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
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/paths"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/internal/utils/bytesutil"
	"github.com/ccfos/huatuo/internal/utils/kmsgutil"
	"github.com/ccfos/huatuo/pkg/metric"

	"github.com/cilium/ebpf"
	"github.com/cloudflare/backoff"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/hungtask.c -o $BPF_DIR/hungtask.o

// HungTaskTracerData is the full data structure.
type HungTaskTracerData struct {
	TID                   uint32 `json:"tid"`
	Comm                  string `json:"comm"`
	CPUsStack             string `json:"cpus_stack"`
	BlockedProcessesStack string `json:"blocked_processes_stack"`
	HungTaskTimeoutSecs   int    `json:"hung_task_timeout_secs"`
}

type hungTaskTracing struct {
	mu              sync.Mutex
	containerCounts map[string]uint64
	backoff         *backoff.Backoff
	nextAllowedTime time.Time
}

func init() {
	// OS such as Fedora-42 may disable this feature.
	if hungTaskTimeout() < 0 {
		return
	}

	tracing.RegisterEventTracing("hungtask", newHungTask)
}

func newHungTask() (*tracing.EventTracingAttr, error) {
	bo := backoff.NewWithoutJitter(3*time.Hour, 10*time.Minute)
	bo.SetDecay(1 * time.Hour)

	return &tracing.EventTracingAttr{
		TracingData: &hungTaskTracing{
			backoff: bo,
		},
		Interval: 10,
		Flag:     tracing.FlagMetric | tracing.FlagTracing,
	}, nil
}

var hungtaskCounter int64

func (c *hungTaskTracing) Update() ([]*metric.Data, error) {
	return c.update(pod.NormalContainers)
}

func (c *hungTaskTracing) update(discover func() (map[string]*pod.Container, error)) ([]*metric.Data, error) {
	data := []*metric.Data{metric.NewCounterData("total", float64(atomic.LoadInt64(&hungtaskCounter)), "hungtask counter", nil)}
	c.mu.Lock()
	empty := len(c.containerCounts) == 0
	c.mu.Unlock()
	if empty {
		return data, nil
	}
	containers, err := discover()
	if err != nil {
		return data, fmt.Errorf("discover hungtask containers: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, count := range c.containerCounts {
		container := containers[id]
		if container == nil {
			delete(c.containerCounts, id)
			continue
		}
		data = append(data, metric.NewContainerCounterData(container, "total", float64(count), "hungtask events attributed to container tasks", nil))
	}
	return data, nil
}

func (c *hungTaskTracing) record(event *abi.HungtaskEvent, discover func() (map[string]*pod.Container, error),
	resolveID func(string) (uint64, error),
) (*pod.Container, error) {
	// Counting precedes trace backoff and best-effort container attribution.
	atomic.AddInt64(&hungtaskCounter, 1)
	if event.CgroupCount == 0 || event.CgroupCount > uint32(len(event.CgroupIds)) {
		return nil, nil
	}
	containers, err := discover()
	if err != nil {
		return nil, err
	}
	container := hungTaskContainer(event, containers, resolveID)
	if container != nil {
		c.mu.Lock()
		if c.containerCounts == nil {
			c.containerCounts = make(map[string]uint64)
		}
		c.containerCounts[container.ID]++
		c.mu.Unlock()
	}
	return container, nil
}

func hungTaskContainer(event *abi.HungtaskEvent, containers map[string]*pod.Container, resolveID func(string) (uint64, error)) *pod.Container {
	if event.CgroupCount == 0 || event.CgroupCount > uint32(len(event.CgroupIds)) {
		return nil
	}
	var matched *pod.Container
	nearest := int(event.CgroupCount)
	for _, container := range containers {
		if container == nil {
			continue
		}
		root := path.Clean(container.CgroupPath)
		if !path.IsAbs(root) || root == "/" {
			continue
		}
		id, err := resolveID(root)
		if err != nil || id == 0 {
			continue
		}
		for i := 0; i < nearest; i++ {
			if event.CgroupIds[i] == id {
				matched, nearest = container, i
				break
			}
		}
	}
	return matched
}

func hungTaskCgroupID(root string) (uint64, error) {
	if cgroups.CgroupMode() == cgroups.Unified {
		return paths.KernfsID(paths.Path(root))
	}
	return paths.KernfsID(paths.Path("cpu", root))
}

func loadHungTaskObject(name string, containers bool) (bpf.BPF, error) {
	spec, err := ebpf.LoadCollectionSpec(filepath.Join(bpf.DefaultObjDir, name))
	if err != nil {
		return nil, err
	}
	if containers {
		delete(spec.Programs, "tracepoint_sched_process_hang")
	} else {
		delete(spec.Programs, "raw_sched_process_hang")
	}
	unified := uint32(0)
	if cgroups.CgroupMode() == cgroups.Unified {
		unified = 1
	}
	return bpf.LoadBPFFromCollectionSpec(name, spec, map[string]any{"unified_cgroups": unified})
}

func startHungTaskBPF(ctx context.Context, name string, load func(string, bool) (bpf.BPF, error)) (bpf.BPF, bpf.PerfEventReader, error) {
	var containerErr error
	for _, containers := range []bool{true, false} {
		obj, err := load(name, containers)
		if err == nil {
			reader, attachErr := obj.AttachAndEventPipe(ctx, "hungtask_perf_events", 8192)
			if attachErr == nil {
				if !containers {
					log.WithError(containerErr).Warn("hungtask container identity unavailable; collecting host events only")
				}
				return obj, reader, nil
			}
			err = attachErr
			if closeErr := obj.Close(); closeErr != nil {
				return nil, nil, errors.Join(containerErr, err, closeErr)
			}
		}
		if !containers {
			return nil, nil, errors.Join(containerErr, fmt.Errorf("start host hungtask: %w", err))
		}
		containerErr = err
	}
	panic("unreachable")
}

func (c *hungTaskTracing) Start(ctx context.Context) error {
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	b, reader, err := startHungTaskBPF(childCtx, bpf.ThisBpfOBJ(), loadHungTaskObject)
	if err != nil {
		return err
	}
	defer b.Close()
	defer reader.Close()

	b.DetachOnContextDone(childCtx, cancel)

	for {
		select {
		case <-childCtx.Done():
			return nil
		default:
			var data abi.HungtaskEvent
			if err := reader.ReadInto(&data); err != nil {
				if errors.Is(err, bpf.ErrPerfEventSamplesLost) {
					log.WithError(err).Warn("lost BPF perf event samples")
					continue
				}
				return fmt.Errorf("hungtask ReadFromPerfEvent: %w", err)
			}

			container, err := c.record(&data, pod.NormalContainers, hungTaskCgroupID)
			if err != nil {
				log.WithError(err).Debug("resolve hung task container")
			}
			containerID := ""
			if container != nil {
				containerID = container.ID
			}

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

			if err := tracing.Save(&tracing.WriteRequest{
				TracerName:        "hungtask",
				ContainerID:       containerID,
				ObservedTimestamp: time.Now().UTC(),
				TracerData: &HungTaskTracerData{
					TID:                   data.TID,
					Comm:                  bytesutil.ToStr(data.Comm[:]),
					CPUsStack:             cpusBT,
					BlockedProcessesStack: blockedProcessesBT,
					HungTaskTimeoutSecs:   hungTaskTimeout(),
				},
			}); err != nil {
				log.Warnf("failed to save tracing data: %v", err)
			}
		}
	}
}

// returns the kernel hung_task_timeout_secs value.
// Returns -1 when the kernel does not support hung task detection
// (CONFIG_DETECT_HUNG_TASK=n), 0 means admin disabled it,
// and the timeout > 0 in seconds means it works.
func hungTaskTimeout() int {
	sysctl := "/proc/sys/kernel/hung_task_timeout_secs"
	data, err := os.ReadFile(sysctl)
	if err != nil {
		return -1
	}
	val, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return -1
	}
	return val
}
