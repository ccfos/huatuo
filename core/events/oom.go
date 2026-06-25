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
	"fmt"
	"sync"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/cgroups"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/utils/bytesutil"
	"huatuo-bamai/internal/utils/kernaddr"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/oom.c -o $BPF_DIR/oom.o

// perfEventData mirrors the BPF perf event struct for OOM events.
type perfEventData struct {
	TriggerComm     [bpf.TaskCommLen]byte
	VictimComm      [bpf.TaskCommLen]byte
	TriggerPid      int32
	VictimPid       int32
	TriggerMemcgCSS uint64
	VictimMemcgCSS  uint64
}

type OOMActor struct {
	MemoryCgroupCSSAddr string                   `json:"memory_cgroup_css_addr"`
	ContainerID         string                   `json:"container_id,omitempty"`
	ContainerHostname   string                   `json:"container_hostname,omitempty"`
	Pid                 int32                    `json:"pid"`
	Comm                string                   `json:"comm"`
	Cgroup              *OOMCgroupMemorySnapshot `json:"cgroup,omitempty"`
}

type OOMTracingData struct {
	Trigger        OOMActor           `json:"trigger"`
	Victim         OOMActor           `json:"victim"`
	MemorySnapshot *OOMMemorySnapshot `json:"memory_snapshot,omitempty"`
}

type oomMetric struct {
	count            int
	latestVictimComm string
}

type oomCollector struct {
	cgroup cgroups.Cgroup
}

var (
	outOfMemoryCounterHost      float64
	outOfMemoryCounterContainer = make(map[string]*oomMetric)
	mutex                       sync.Mutex
)

func init() {
	tracing.RegisterEventTracing("oom", newOOMCollector)
}

func newOOMCollector() (*tracing.EventTracingAttr, error) {
	cgroup, err := cgroups.NewManager()
	if err != nil {
		log.Warnf("failed to initialize cgroup reader for oom snapshot: %v", err)
	}

	return &tracing.EventTracingAttr{
		TracingData: &oomCollector{
			cgroup: cgroup,
		},
		Interval: 10,
		Flag:     tracing.FlagTracing | tracing.FlagMetric,
	}, nil
}

func (c *oomCollector) Update() ([]*metric.Data, error) {
	containers, err := pod.NormalContainers()
	if err != nil {
		return nil, fmt.Errorf("get normal container: %w", err)
	}

	var metrics []*metric.Data

	mutex.Lock()

	metrics = append(metrics, metric.NewCounterData("host_total", outOfMemoryCounterHost, "host oom counter", nil))
	for _, container := range containers {
		if val, exists := outOfMemoryCounterContainer[container.ID]; exists {
			metrics = append(
				metrics,
				metric.NewContainerCounterData(container, "total", float64(val.count), "containers oom counter", map[string]string{"latest_victim_comm": val.latestVictimComm}),
			)
		}
	}

	mutex.Unlock()
	return metrics, nil
}

func (c *oomCollector) Start(ctx context.Context) error {
	b, err := bpf.LoadBpf(bpf.ThisBpfOBJ(), nil)
	if err != nil {
		return err
	}
	defer b.Close()

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	reader, err := b.AttachAndEventPipe(childCtx, "oom_perf_events", 8192)
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
			var data perfEventData
			if err := reader.ReadInto(&data); err != nil {
				return fmt.Errorf("failed to read perf event: %w", err)
			}

			containers, err := pod.Containers()
			if err != nil {
				return fmt.Errorf("failed to fetch containers: %w", err)
			}

			oomData := buildTracingData(data, containers, c.cgroup)

			mutex.Lock()

			if container, ok := containers[oomData.Victim.ContainerID]; ok {
				containerCounterUpdate(container.ID, oomData.Victim.Comm)
			} else {
				outOfMemoryCounterHost++
			}

			mutex.Unlock()

			if err := tracing.Save(&tracing.WriteRequest{
				TracerName:  "oom",
				TracerTime:  time.Now(),
				TracerData:  oomData,
				ContainerID: oomData.Victim.ContainerID,
			}); err != nil {
				log.Warnf("failed to save tracing data: %v", err)
			}
		}
	}
}

func buildTracingData(data perfEventData, containers map[string]*pod.Container, cgroup cgroups.Cgroup) *OOMTracingData {
	cssContainers := pod.BuildCssContainersID(containers, pod.SubSysMemory)

	triggerID := cssContainers[data.TriggerMemcgCSS]
	victimID := cssContainers[data.VictimMemcgCSS]

	oomData := &OOMTracingData{
		Trigger: OOMActor{
			MemoryCgroupCSSAddr: kernaddr.Format(data.TriggerMemcgCSS),
			ContainerID:         triggerID,
			Pid:                 data.TriggerPid,
			Comm:                bytesutil.ToStr(data.TriggerComm[:]),
		},
		Victim: OOMActor{
			MemoryCgroupCSSAddr: kernaddr.Format(data.VictimMemcgCSS),
			ContainerID:         victimID,
			Pid:                 data.VictimPid,
			Comm:                bytesutil.ToStr(data.VictimComm[:]),
		},
	}

	if container, ok := containers[triggerID]; ok {
		oomData.Trigger.ContainerHostname = container.Hostname
		if snap, err := cgroupMemorySnapshot(cgroup, container); err != nil {
			log.Warnf("trigger cgroup snapshot: %v", err)
		} else {
			oomData.Trigger.Cgroup = snap
		}
	}

	if container, ok := containers[victimID]; ok {
		oomData.Victim.ContainerHostname = container.Hostname
		if snap, err := cgroupMemorySnapshot(cgroup, container); err != nil {
			log.Warnf("victim cgroup snapshot: %v", err)
		} else {
			oomData.Victim.Cgroup = snap
		}
	}

	if snap, err := hostMemorySnapshot(); err != nil {
		log.Warnf("host memory snapshot: %v", err)
	} else {
		oomData.MemorySnapshot = snap
	}
	return oomData
}

func containerCounterUpdate(containerID, comm string) {
	if val, exists := outOfMemoryCounterContainer[containerID]; exists {
		val.count++
		val.latestVictimComm = comm
		return
	}

	outOfMemoryCounterContainer[containerID] = &oomMetric{count: 1, latestVictimComm: comm}
}
