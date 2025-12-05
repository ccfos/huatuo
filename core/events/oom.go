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
	"sync"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/storage"
	"huatuo-bamai/internal/utils/bytesutil"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/oom.c -o $BPF_DIR/oom.o

type perfEventData struct {
	TriggerProcessName [bpf.TaskCommLen]byte
	VictimProcessName  [bpf.TaskCommLen]byte
	TriggerPid         int32
	VictimPid          int32
	TriggerMemcgCSS    uint64
	VictimMemcgCSS     uint64
}

type OOMTracingData struct {
	TriggerMemcgCSS          string `json:"trigger_memcg_css"`
	TriggerContainerID       string `json:"trigger_container_id"`
	TriggerContainerHostname string `json:"trigger_container_hostname"`
	TriggerPid               int32  `json:"trigger_pid"`
	TriggerProcessName       string `json:"trigger_process_name"`
	VictimMemcgCSS           string `json:"victim_memcg_css"`
	VictimContainerID        string `json:"victim_container_id"`
	VictimContainerHostname  string `json:"victim_container_hostname"`
	VictimPid                int32  `json:"victim_pid"`
	VictimProcessName        string `json:"victim_process_name"`
}

type oomMetric struct {
	count             int
	victimProcessName string
}

type oomCollector struct{}

var (
	outOfMemoryCounterHost      float64
	outOfMemoryCounterContainer = make(map[string]*oomMetric)
	mutex                       sync.Mutex
)

func init() {
	tracing.RegisterEventTracing("oom", newOOMCollector)
}

func newOOMCollector() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &oomCollector{},
		Interval:    10,
		Flag:        tracing.FlagTracing | tracing.FlagMetric,
	}, nil
}

func (c *oomCollector) Update() ([]*metric.Data, error) {
	containers, err := pod.NormalContainers()
	if err != nil {
		return nil, fmt.Errorf("get normal container: %w", err)
	}

	metrics := []*metric.Data{}

	mutex.Lock()

	metrics = append(metrics, metric.NewCounterData("host_total", outOfMemoryCounterHost, "host oom counter", nil))
	for _, container := range containers {
		if val, exists := outOfMemoryCounterContainer[container.ID]; exists {
			metrics = append(metrics,
				metric.NewContainerCounterData(container, "total", float64(val.count), "containers oom counter", map[string]string{"process": val.victimProcessName}),
			)
		}
	}

	mutex.Unlock()
	return metrics, nil
}

// Info return case's base info
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
				return fmt.Errorf("ReadFromPerfEvent fail: %w", err)
			}

			containers, err := pod.Containers()
			if err != nil {
				return fmt.Errorf("fetching the containers, err: %w", err)
			}

			cssContainers := pod.BuildCssContainersID(containers, pod.SubSysMemory)
			oomData := &OOMTracingData{
				TriggerMemcgCSS:    fmt.Sprintf("0x%x", data.TriggerMemcgCSS),
				TriggerPid:         data.TriggerPid,
				TriggerProcessName: bytesutil.ToString(data.TriggerProcessName[:]),
				TriggerContainerID: cssContainers[data.TriggerMemcgCSS],
				VictimMemcgCSS:     fmt.Sprintf("0x%x", data.VictimMemcgCSS),
				VictimPid:          data.VictimPid,
				VictimProcessName:  bytesutil.ToString(data.VictimProcessName[:]),
				VictimContainerID:  cssContainers[data.VictimMemcgCSS],
			}

			// leave the hostname empty if this is not container.
			if container, ok := containers[oomData.TriggerContainerID]; ok {
				oomData.TriggerContainerHostname = container.Hostname
			}

			// update victim hostname and metric counters
			mutex.Lock()

			if container, ok := containers[oomData.VictimContainerID]; ok {
				oomData.VictimContainerHostname = container.Hostname
				containerCounterUpdate(container.ID, oomData.VictimProcessName)
			} else {
				outOfMemoryCounterHost++
			}

			mutex.Unlock()

			storage.Save("oom", "", time.Now(), oomData)
		}
	}
}

func containerCounterUpdate(containerID, processName string) {
	if val, exists := outOfMemoryCounterContainer[containerID]; exists {
		val.count++
		val.victimProcessName = processName
		return
	}

	outOfMemoryCounterContainer[containerID] = &oomMetric{count: 1, victimProcessName: processName}
}
