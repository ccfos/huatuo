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

package collector

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

func init() {
	tracing.RegisterEventTracing("memory_reclaim", newMemoryCgroup)
}

func newMemoryCgroup() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &memoryCgroup{},
		Internal:    10,
		Flag:        tracing.FlagTracing | tracing.FlagMetric,
	}, nil
}

type memoryCgroupMetric struct {
	DirectstallCount uint64
}

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/memory_reclaim.c -o $BPF_DIR/memory_reclaim.o

type memoryCgroup struct {
	bpf      bpf.BPF
	isRuning bool
}

func (c *memoryCgroup) Update() ([]*metric.Data, error) {
	if !c.isRuning {
		return nil, nil
	}

	containersMap := make(map[uint64]*pod.Container)
	containers, err := pod.GetNormalContainers()
	if err != nil {
		return nil, fmt.Errorf("get container: %w", err)
	}

	for _, container := range containers {
		containersMap[container.CSS["memory"]] = container
	}

	items, err := c.bpf.DumpMapByName("mem_cgroup_map")
	if err != nil {
		return nil, fmt.Errorf("dump mem_cgroup_map: %w", err)
	}

	var (
		cgroupMetric     memoryCgroupMetric
		containersMetric []*metric.Data
		css              uint64
	)
	for _, v := range items {
		keyBuf := bytes.NewReader(v.Key)
		if err := binary.Read(keyBuf, binary.LittleEndian, &css); err != nil {
			return nil, fmt.Errorf("mem_cgroup_map key: %w", err)
		}

		valBuf := bytes.NewReader(v.Value)
		if err := binary.Read(valBuf, binary.LittleEndian, &cgroupMetric); err != nil {
			return nil, fmt.Errorf("mem_cgroup_map value: %w", err)
		}

		if container, exist := containersMap[css]; exist {
			containersMetric = append(containersMetric,
				metric.NewContainerGaugeData(container, "directstall",
					float64(cgroupMetric.DirectstallCount),
					"counting of cgroup try_charge reclaim", nil))
		}
	}

	// if events haven't happened, upload zero for all containers.
	if len(items) == 0 {
		for _, container := range containersMap {
			containersMetric = append(containersMetric,
				metric.NewContainerGaugeData(container, "directstall", float64(0),
					"counting of cgroup try_charge reclaim", nil))
		}
	}

	return containersMetric, nil
}

func (c *memoryCgroup) Start(ctx context.Context) error {
	var err error
	c.bpf, err = bpf.LoadBpf(bpf.ThisBpfOBJ(), nil)
	if err != nil {
		return fmt.Errorf("load bpf: %w", err)
	}
	defer c.bpf.Close()

	if err = c.bpf.Attach(); err != nil {
		return fmt.Errorf("attach: %w", err)
	}

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	c.bpf.WaitDetachByBreaker(childCtx, cancel)
	c.isRuning = true
	<-childCtx.Done()
	c.isRuning = false
	return nil
}
