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
	"fmt"

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

	return c.update(lease, pod.NormalContainers)
}

func (c *memoryCgroupReclaim) update(obj bpf.BPF, discover func() (map[string]*pod.Container, error)) ([]*metric.Data, error) {
	items, hostErr := obj.DumpMapByName("memory_host_directstall")
	var data []*metric.Data
	if hostErr != nil {
		hostErr = fmt.Errorf("dump host memcg reclaim count: %w", hostErr)
	} else {
		var count uint64
		count, hostErr = hostMemcgReclaimCount(items)
		if hostErr == nil {
			data = append(data, metric.NewGaugeData("directstall", float64(count),
				"cumulative host-wide memcg reclaim events, excluding kswapd", nil))
		}
	}

	containers, err := discover()
	if err != nil {
		return data, errors.Join(hostErr, fmt.Errorf("discover memcg reclaim containers: %w", err))
	}

	containersCssMem := pod.BuildCssContainers(containers, subsystem.SubsystemMemory)

	items, err = obj.DumpMapByName("memory_cgroup_allocpages_stall")
	if err != nil {
		return data, errors.Join(hostErr, fmt.Errorf("dump container memcg reclaim count: %w", err))
	}

	var (
		reclaimVal memoryBpfStruct
		cssAddr    uint64
	)
	for _, v := range items {
		keyBuf := bytes.NewReader(v.Key)
		if err := binary.Read(keyBuf, binary.LittleEndian, &cssAddr); err != nil {
			return data, errors.Join(hostErr, err)
		}

		valBuf := bytes.NewReader(v.Value)
		if err := binary.Read(valBuf, binary.LittleEndian, &reclaimVal); err != nil {
			return data, errors.Join(hostErr, err)
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

	return data, hostErr
}

func hostMemcgReclaimCount(items []bpf.MapItem) (uint64, error) {
	if len(items) != 1 || len(items[0].Key) != 4 || binary.LittleEndian.Uint32(items[0].Key) != 0 ||
		len(items[0].Value) == 0 || len(items[0].Value)%8 != 0 {
		return 0, fmt.Errorf("host memcg reclaim map: expected key zero and one uint64 per CPU")
	}
	var count uint64
	for value := items[0].Value; len(value) > 0; value = value[8:] {
		count += binary.LittleEndian.Uint64(value)
	}
	return count, nil
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
