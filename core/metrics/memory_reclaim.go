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
	"sync/atomic"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/cgroups/subsystem"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

func init() {
	tracing.RegisterEventTracing("memory_reclaim", newMemoryCgroupReclaim)
}

func newMemoryCgroupReclaim() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &memoryCgroupReclaim{},
		Interval:    10,
		Flag:        tracing.FlagTracing | tracing.FlagMetric,
	}, nil
}

type memoryBpfStruct struct {
	DirectstallCount uint64
}

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/memory_reclaim.c -o $BPF_DIR/memory_reclaim.o

type memoryCgroupReclaim struct {
	bpf     bpf.BPF
	running atomic.Bool
}

func (c *memoryCgroupReclaim) Update() ([]*metric.Data, error) {
	if !c.running.Load() {
		return nil, nil
	}

	containers, err := pod.NormalContainers()
	if err != nil {
		return nil, err
	}

	containersCssMem := pod.BuildCssContainers(containers, subsystem.SubsystemMemory)

	items, err := c.bpf.DumpMapByName("memory_cgroup_allocpages_stall")
	if err != nil {
		return nil, err
	}

	var (
		reclaimVal memoryBpfStruct
		cssAddr    uint64
		data       []*metric.Data
	)
	for _, v := range items {
		keyBuf := bytes.NewReader(v.Key)
		if err := binary.Read(keyBuf, binary.LittleEndian, &cssAddr); err != nil {
			return nil, err
		}

		valBuf := bytes.NewReader(v.Value)
		if err := binary.Read(valBuf, binary.LittleEndian, &reclaimVal); err != nil {
			return nil, err
		}

		if container, exist := containersCssMem[cssAddr]; exist {
			data = append(data, metric.NewContainerGaugeData(container, "directstall",
				float64(reclaimVal.DirectstallCount), "counter of cgroup reclaim when try_charge", nil))
		}
	}

	// if events haven't happened, upload zero for all containers.
	if len(items) == 0 {
		for _, container := range containersCssMem {
			data = append(data, metric.NewContainerGaugeData(container, "directstall",
				float64(0), "counter of cgroup reclaim when try_charge", nil))
		}
	}

	return data, nil
}

func (c *memoryCgroupReclaim) Start(ctx context.Context) error {
	obj, err := bpf.LoadBpf(bpf.ThisBpfOBJ(), nil)
	if err != nil {
		return err
	}
	defer obj.Close()

	if err := obj.Attach(); err != nil {
		return err
	}

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	obj.WaitDetachByBreaker(childCtx, cancel)

	c.bpf = obj
	c.running.Store(true)

	// wait stop
	<-childCtx.Done()
	c.running.Store(false)
	return nil
}
