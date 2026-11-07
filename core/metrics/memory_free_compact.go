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

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/metric"
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

func (c *reclaimCompact) Update() ([]*metric.Data, error) {
	lease, ok := c.bpf.Acquire()
	if !ok {
		return nil, nil
	}
	defer lease.Release()

	items, err := lease.DumpMapByName("mm_free_compact_map")
	if err != nil {
		return nil, fmt.Errorf("dump memory stall map: %w", err)
	}
	if !pod.KubeletPodListRunningEnabled() {
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
		if container == nil {
			data = append(data,
				metric.NewGaugeData("compaction_stall", float64(mm.CompactionStat)/1e6, "cumulative milliseconds stalled in memory compaction", nil),
				metric.NewGaugeData("allocpages_stall", float64(mm.AllocstallStat)/1e6, "cumulative milliseconds stalled in global direct reclaim", nil))
		} else {
			data = append(data,
				metric.NewContainerGaugeData(container, "compaction_stall", float64(mm.CompactionStat)/1e6, "cumulative milliseconds stalled in memory compaction", nil),
				metric.NewContainerGaugeData(container, "allocpages_stall", float64(mm.AllocstallStat)/1e6, "cumulative milliseconds stalled in global direct reclaim", nil))
		}
	}
	if !hostFound {
		decodeErr = errors.Join(decodeErr, errors.New("memory stall map: reserved host entry missing or invalid"))
	}
	return data, decodeErr
}

// Start detect work, load bpf and wait data
func (c *reclaimCompact) Start(ctx context.Context) (retErr error) {
	obj, err := bpf.LoadBPF(bpf.ThisBpfOBJ(), nil)
	if err != nil {
		return err
	}

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
		{
			ProgramName: "kprobe_mem_cgroup_css_released",
			Symbol:      "mem_cgroup_css_released",
		},
	}
	mapID := obj.MapIDByName("mm_free_compact_map")
	if mapID == 0 {
		return errors.Join(fmt.Errorf("required map %q missing", "mm_free_compact_map"), obj.Close())
	}
	// Reserve host totals before probes can fill the map; an empty per-CPU
	// slice is zero-padded by the loader for every possible CPU.
	if err := obj.WriteMapItems(mapID, []bpf.MapItem{{Key: make([]byte, 8), Value: []byte{}}}); err != nil {
		return errors.Join(err, obj.Close())
	}

	if err := obj.AttachWithOptions(opts); err != nil {
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

	defer obj.DetachOnContextDone(childCtx, cancel)()

	// wait stop
	<-childCtx.Done()
	return nil
}
