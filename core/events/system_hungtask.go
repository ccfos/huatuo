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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/timeutil"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/internal/utils/bytesutil"
	"github.com/ccfos/huatuo/internal/utils/kmsgutil"
	"github.com/ccfos/huatuo/pkg/metric"

	"github.com/cilium/ebpf"
	"github.com/cloudflare/backoff"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/system_hungtask.c -o $BPF_DIR/system_hungtask.o

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
	// Keep discovery, pruning and the count snapshot ordered with event updates.
	c.mu.Lock()
	defer c.mu.Unlock()
	data := []*metric.Data{metric.NewContainerCounterData(nil, "total", float64(atomic.LoadInt64(&hungtaskCounter)), "hungtask counter", nil)}
	if len(c.containerCounts) == 0 {
		return data, nil
	}
	containers, err := pod.NormalContainers()
	if err != nil {
		return data, fmt.Errorf("discover hungtask containers: %w", err)
	}
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
	return bpf.LoadBPFFromCollectionSpec(name, spec, nil)
}

func startHungTaskBPF(ctx context.Context, name string) (bpf.BPF, bpf.PerfEventReader, error) {
	var containerErr error
	for _, containers := range []bool{true, false} {
		obj, err := loadHungTaskObject(name, containers)
		if err == nil {
			reader, attachErr := obj.AttachAndEventPipe(ctx, "hungtask_perf_events", bpf.DefaultPerfEventBufferBytes)
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

	b, reader, err := startHungTaskBPF(childCtx, bpf.ThisBpfOBJ())
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

			// Count every report before attribution and trace backoff.
			atomic.AddInt64(&hungtaskCounter, 1)
			container, err := pod.ContainerByCSS(data.CPUCSSAddr, subsystem.SubsystemCPU)
			if err != nil {
				log.WithError(err).Debug("resolve hung task container")
			}
			containerID := ""
			if container != nil {
				containerID = container.ID
				c.mu.Lock()
				if c.containerCounts == nil {
					c.containerCounts = make(map[string]uint64)
				}
				c.containerCounts[containerID]++
				c.mu.Unlock()
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
				ObservedTimestamp: timeutil.Now(),
				ContainerID:       containerID,
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
