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
	"sync"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/storage"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/oom.c -o $BPF_DIR/oom.o

type perfEventData struct {
	TriggerProcessName [16]byte
	VictimProcessName  [16]byte
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

	VictimMemcgCSS          string `json:"victim_memcg_css"`
	VictimContainerID       string `json:"victim_container_id"`
	VictimContainerHostname string `json:"victim_container_hostname"`
	VictimPid               int32  `json:"victim_pid"`
	VictimProcessName       string `json:"victim_process_name"`
}

type oomMetric struct {
	count             int
	victimProcessName string
}

type oomCollector struct{}

func init() {
	tracing.RegisterEventTracing("oom", newOOMCollector)
}

func newOOMCollector() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &oomCollector{},
		Internal:    10,
		Flag:        tracing.FlagTracing | tracing.FlagMetric,
	}, nil
}

var (
	hostOOMCounter      float64
	containerOOMCounter = make(map[string]oomMetric)
	mutex               sync.Mutex
)

func (c *oomCollector) Update() ([]*metric.Data, error) {
	containers, err := pod.GetNormalContainers()
	if err != nil {
		return nil, fmt.Errorf("get normal container: %w", err)
	}
	metrics := []*metric.Data{}
	mutex.Lock()
	metrics = append(metrics, metric.NewGaugeData("host_counter", hostOOMCounter, "host oom counter", nil))
	for _, container := range containers {
		if val, exists := containerOOMCounter[container.ID]; exists {
			metrics = append(metrics,
				metric.NewContainerGaugeData(container, "counter", float64(val.count), "containers oom counter", map[string]string{"process": val.victimProcessName}),
			)
		}
	}

	containerOOMCounter = make(map[string]oomMetric)
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
			cssToCtMap, err := pod.GetCSSToContainerID("memory")
			if err != nil {
				log.Errorf("failed to GetCSSToContainerID, err: %v", err)
				continue
			}
			cts, err := pod.GetAllContainers()
			if err != nil {
				log.Errorf("Can't get GetAllContainers, err: %v", err)
				return err
			}
			caseData := &OOMTracingData{
				TriggerMemcgCSS:    fmt.Sprintf("0x%x", data.TriggerMemcgCSS),
				TriggerPid:         data.TriggerPid,
				TriggerProcessName: strings.TrimRight(string(data.TriggerProcessName[:]), "\x00"),
				TriggerContainerID: cssToCtMap[data.TriggerMemcgCSS],
				VictimMemcgCSS:     fmt.Sprintf("0x%x", data.VictimMemcgCSS),
				VictimPid:          data.VictimPid,
				VictimProcessName:  strings.TrimRight(string(data.VictimProcessName[:]), "\x00"),
				VictimContainerID:  cssToCtMap[data.VictimMemcgCSS],
			}

			if caseData.TriggerContainerID == "" {
				caseData.TriggerContainerID = "None"
				caseData.TriggerContainerHostname = "Non-Container Cgroup"
			} else {
				caseData.TriggerContainerHostname = cts[caseData.TriggerContainerID].Hostname
				if caseData.TriggerContainerHostname == "" {
					caseData.TriggerContainerHostname = "unknown"
				}
			}
			mutex.Lock()
			if caseData.VictimContainerID == "" {
				hostOOMCounter++
				caseData.VictimContainerID = "None"
				caseData.VictimContainerHostname = "Non-Container Cgroup"
			} else {
				if val, exists := containerOOMCounter[cts[caseData.VictimContainerID].ID]; exists {
					val.count++
					val.victimProcessName = val.victimProcessName + "," + caseData.VictimProcessName
					containerOOMCounter[cts[caseData.VictimContainerID].ID] = val
				} else {
					containerOOMCounter[cts[caseData.VictimContainerID].ID] = oomMetric{
						count:             1,
						victimProcessName: caseData.VictimProcessName,
					}
				}
				caseData.VictimContainerHostname = cts[caseData.VictimContainerID].Hostname
				if caseData.VictimContainerHostname == "" {
					caseData.VictimContainerHostname = "unknown"
				}
			}
			mutex.Unlock()

			storage.Save("oom", "", time.Now(), caseData)
		}
	}
}
