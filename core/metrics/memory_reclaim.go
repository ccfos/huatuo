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
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/cilium/ebpf"

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

	items, err := lease.DumpMapByName("memory_cgroup_allocpages_stall")
	if err != nil {
		return nil, fmt.Errorf("dump memcg reclaim count: %w", err)
	}
	// A nonzero host count without container rows may reflect lost metadata or
	// failed map insertion. Only a pristine zero snapshot preserves zero-fill.
	zeroSnapshot := false
	var data []*metric.Data
	var decodeErr error
	if len(items) == 1 && len(items[0].Key) == 8 && binary.LittleEndian.Uint64(items[0].Key) == 0 {
		data, decodeErr = memcgReclaimMetrics(items, nil)
		if decodeErr != nil || len(data) != 1 || data[0].Value != 0 {
			return data, decodeErr
		}
		zeroSnapshot = true
	}
	containers, err := pod.NormalContainers()
	if err != nil {
		containers = nil
		err = fmt.Errorf("discover memcg reclaim containers: %w", err)
	}
	cssContainers := pod.BuildCssContainers(containers, subsystem.SubsystemMemory)
	if zeroSnapshot {
		for _, container := range cssContainers {
			data = append(data, metric.NewContainerGaugeData(container, "directstall", 0,
				"cumulative memcg reclaim events, excluding kswapd", nil))
		}
	} else {
		data, decodeErr = memcgReclaimMetrics(items, cssContainers)
	}
	return data, errors.Join(decodeErr, err)
}

func memcgReclaimMetrics(items []bpf.MapItem, containers map[uint64]*pod.Container) ([]*metric.Data, error) {
	data := make([]*metric.Data, 0, len(containers)+1)
	var decodeErr error
	hostFound := false
	for _, item := range items {
		if len(item.Key) != 8 || len(item.Value) == 0 || len(item.Value)%8 != 0 {
			decodeErr = errors.Join(decodeErr, errors.New("memcg reclaim map: expected 8-byte CSS key and one uint64 per CPU"))
			continue
		}
		css := binary.LittleEndian.Uint64(item.Key)
		var container *pod.Container
		if css != 0 {
			container = containers[css]
			if container == nil {
				continue
			}
		}
		var count uint64
		for value := item.Value; len(value) > 0; value = value[8:] {
			count += binary.LittleEndian.Uint64(value)
		}
		hostFound = hostFound || css == 0
		data = append(data, metric.NewContainerGaugeData(container, "directstall", float64(count),
			"cumulative memcg reclaim events, excluding kswapd", nil))
	}
	if !hostFound {
		decodeErr = errors.Join(decodeErr, errors.New("memcg reclaim map: reserved host entry missing or invalid"))
	}
	return data, decodeErr
}

func loadMemoryReclaimObject(name string) (bpf.BPF, error) {
	spec, err := ebpf.LoadCollectionSpec(filepath.Join(bpf.DefaultObjDir, name))
	if err != nil {
		return nil, err
	}
	if err := requireMemoryMap(name, spec, "memory_cgroup_allocpages_stall"); err != nil {
		return nil, err
	}
	cpus, err := ebpf.PossibleCPU()
	if err != nil {
		return nil, err
	}
	spec.Maps["memory_cgroup_allocpages_stall"].Contents = []ebpf.MapKV{{
		Key:   uint64(0),
		Value: make([]uint64, cpus),
	}}
	return bpf.LoadBPFFromCollectionSpec(name, spec, nil)
}

func (c *memoryCgroupReclaim) Start(ctx context.Context) (retErr error) {
	obj, err := loadMemoryReclaimObject(bpf.ThisBpfOBJ())
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
