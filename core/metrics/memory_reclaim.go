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

package collector

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/metric"
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
	bpf bpf.Reference
}

func (c *memoryCgroupReclaim) Update() ([]*metric.Data, error) {
	lease, ok := c.bpf.Acquire()
	if !ok {
		return nil, nil
	}
	defer lease.Release()

	containers, err := pod.NormalContainers()
	if err != nil {
		return nil, err
	}

	containersCssMem := pod.BuildCssContainers(containers, subsystem.SubsystemMemory)

	items, err := lease.DumpMapByName("memory_cgroup_allocpages_stall")
	if err != nil {
		return nil, err
	}

	return buildReclaimMetrics(items, containersCssMem)
}

// buildReclaimMetrics turns dumped BPF map items keyed by memory-cgroup css
// address into per-container directstall metrics.
func buildReclaimMetrics(items []bpf.MapItem, containersCssMem map[uint64]*pod.Container) ([]*metric.Data, error) {
	var (
		reclaimVal memoryBpfStruct
		cssAddr    uint64
		data       []*metric.Data
		reported   = make(map[uint64]struct{}, len(items))
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
			reported[cssAddr] = struct{}{}
			data = append(data, metric.NewContainerGaugeData(container, "directstall",
				float64(reclaimVal.DirectstallCount), "counter of cgroup reclaim when try_charge", nil))
		}
	}

	// Upload zero for the containers whose events haven't happened, so their
	// series stay present once any other container starts reclaiming.
	for css, container := range containersCssMem {
		if _, ok := reported[css]; !ok {
			data = append(data, metric.NewContainerGaugeData(container, "directstall",
				float64(0), "counter of cgroup reclaim when try_charge", nil))
		}
	}

	return data, nil
}

func (c *memoryCgroupReclaim) Start(ctx context.Context) (retErr error) {
	obj, err := bpf.LoadBPF(bpf.ThisBpfOBJ(), nil)
	if err != nil {
		return err
	}

	if err := obj.Attach(); err != nil {
		return errors.Join(err, obj.Close())
	}
	if err := c.bpf.Publish(obj); err != nil {
		return errors.Join(err, obj.Close())
	}
	defer func() {
		retErr = errors.Join(retErr, c.bpf.UnPublish())
	}()

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	obj.DetachOnContextDone(childCtx, cancel)

	// wait stop
	<-childCtx.Done()
	return nil
}
