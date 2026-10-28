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

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/metric"

	"github.com/cilium/ebpf"
)

func init() {
	tracing.RegisterEventTracing("memory_free", newReclaimCompact)
}

func newReclaimCompact() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &reclaimCompact{},
		Interval:    10,
		Flag:        tracing.FlagTracing | tracing.FlagMetric,
	}, nil
}

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/memory_free_compact.c -o $BPF_DIR/memory_free_compact.o

type reclaimCompact struct {
	bpf bpf.Reference
}

// Keep the mode with the leased object across stop/start and fallback.
type memoryStallObject struct {
	bpf.BPF
	containers bool
}

func (c *reclaimCompact) Update() ([]*metric.Data, error) {
	lease, ok := c.bpf.Acquire()
	if !ok {
		return nil, nil
	}
	defer lease.Release()

	obj := lease.BPF.(*memoryStallObject)
	items, err := obj.DumpMapByName("mm_free_compact_map")
	if err != nil {
		return nil, fmt.Errorf("dump memory stall map: %w", err)
	}
	// A host-only snapshot needs no container metadata or CSS index.
	if !obj.containers || len(items) == 1 && len(items[0].Key) == 8 && binary.LittleEndian.Uint64(items[0].Key) == 0 {
		return containerMemoryStalls(items, nil)
	}
	containers, err := pod.NormalContainers()
	if err != nil {
		containers = nil
		err = fmt.Errorf("discover memory stall containers: %w", err)
	}
	data, decodeErr := containerMemoryStalls(items, pod.BuildCssContainers(containers, subsystem.SubsystemMemory))
	return data, errors.Join(decodeErr, err)
}

func decodeMemoryLatency(value []byte) (abi.MmFreeCompactEntry, error) {
	var total abi.MmFreeCompactEntry
	if len(value) == 0 || len(value)%abi.MmFreeCompactEntrySize != 0 {
		return total, fmt.Errorf("memory stall map: expected one memory stall counter per CPU")
	}
	for len(value) > 0 {
		total.CompactionStat += binary.LittleEndian.Uint64(value)
		total.AllocstallStat += binary.LittleEndian.Uint64(value[8:])
		value = value[abi.MmFreeCompactEntrySize:]
	}
	return total, nil
}

func containerMemoryStalls(items []bpf.MapItem, containers map[uint64]*pod.Container) ([]*metric.Data, error) {
	data := make([]*metric.Data, 0, 2*(len(containers)+1))
	var decodeErr error
	hostFound := false
	for _, item := range items {
		if len(item.Key) != 8 {
			decodeErr = errors.Join(decodeErr, fmt.Errorf("memory stall map: expected 8-byte CSS key"))
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
		mm, err := decodeMemoryLatency(item.Value)
		if err != nil {
			decodeErr = errors.Join(decodeErr, err)
			continue
		}
		hostFound = hostFound || css == 0
		data = append(data,
			metric.NewContainerGaugeData(container, "compaction_stall", float64(mm.CompactionStat)/1e6, "cumulative milliseconds stalled in memory compaction", nil),
			metric.NewContainerGaugeData(container, "allocpages_stall", float64(mm.AllocstallStat)/1e6, "cumulative milliseconds stalled in global direct reclaim", nil))
	}
	if !hostFound {
		decodeErr = errors.Join(decodeErr, errors.New("memory stall map: reserved host entry missing or invalid"))
	}
	return data, decodeErr
}

// Start detect work, load bpf and wait data
func (c *reclaimCompact) Start(ctx context.Context) (retErr error) {
	obj, err := loadMemoryStalls(bpf.ThisBpfOBJ())
	if err != nil {
		return err
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

func loadMemoryStallObject(name string, containers bool) (bpf.BPF, error) {
	spec, err := ebpf.LoadCollectionSpec(filepath.Join(bpf.DefaultObjDir, name))
	if err != nil {
		return nil, err
	}
	if err := requireMemoryMap(name, spec, "mm_free_compact_map"); err != nil {
		return nil, err
	}
	enabled := uint32(1)
	if !containers {
		enabled = 0
		spec.Maps["mm_free_compact_map"].MaxEntries = 1
		delete(spec.Programs, "kprobe_mem_cgroup_css_released")
	}
	cpus, err := ebpf.PossibleCPU()
	if err != nil {
		return nil, err
	}
	// Reserve host totals before probes can consume the bounded hash capacity.
	spec.Maps["mm_free_compact_map"].Contents = []ebpf.MapKV{{
		Key:   uint64(0),
		Value: make([]abi.MmFreeCompactEntry, cpus),
	}}
	return bpf.LoadBPFFromCollectionSpec(name, spec, map[string]any{"enable_container_stalls": enabled})
}

func loadMemoryStalls(name string) (*memoryStallObject, error) {
	var containerErr error
	for _, containers := range []bool{true, false} {
		obj, err := loadMemoryStallObject(name, containers)
		if err == nil {
			// Attach completion probes before recording operation starts.
			opts := []bpf.AttachOption{
				{
					ProgramName: "tracepoint_try_to_free_pages_end",
					Symbol:      "vmscan/mm_vmscan_direct_reclaim_end",
				},
				{
					ProgramName: "kretprobe_try_to_compact_pages_host",
					Symbol:      "try_to_compact_pages",
				},
				{
					ProgramName: "tracepoint_try_to_free_pages_begin",
					Symbol:      "vmscan/mm_vmscan_direct_reclaim_begin",
				},
				{
					ProgramName: "kprobe_try_to_compact_pages_host",
					Symbol:      "try_to_compact_pages",
				},
			}
			if containers {
				opts = append(opts, bpf.AttachOption{
					ProgramName: "kprobe_mem_cgroup_css_released",
					Symbol:      "mem_cgroup_css_released",
				})
			}
			err = obj.AttachWithOptions(opts)
			if err == nil {
				if !containers {
					log.WithError(containerErr).Warn("container memory stalls unavailable; collecting host stalls only")
				}
				return &memoryStallObject{
					BPF:        obj,
					containers: containers,
				}, nil
			}
			if closeErr := obj.Close(); closeErr != nil {
				return nil, errors.Join(containerErr, err, closeErr)
			}
		}
		if containers {
			containerErr = err
		} else {
			return nil, errors.Join(containerErr, fmt.Errorf("load host memory stalls: %w", err))
		}
	}
	panic("unreachable")
}

func requireMemoryMap(object string, spec *ebpf.CollectionSpec, name string) error {
	if spec.Maps[name] == nil {
		return fmt.Errorf("BPF object %q: required map %q missing", object, name)
	}
	return nil
}
