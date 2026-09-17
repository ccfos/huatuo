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
	"os"
	"path/filepath"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/paths"
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

type memoryLatency struct {
	/* the host latency counters of compaction and alloc pages in direct reclaim. */
	CompactionStall uint64
	AllocPagesStall uint64
}

func (c *reclaimCompact) Update() ([]*metric.Data, error) {
	lease, ok := c.bpf.Acquire()
	if !ok {
		return nil, nil
	}
	defer lease.Release()

	return c.update(lease.BPF.(*memoryStallObject), pod.NormalContainers)
}

func (c *reclaimCompact) update(obj *memoryStallObject, discover func() (map[string]*pod.Container, error)) ([]*metric.Data, error) {
	items, err := obj.DumpMapByName("mm_free_compact_map")
	if err != nil {
		return nil, fmt.Errorf("dump map mm_free_compact_map: %w", err)
	}
	if len(items) != 1 || len(items[0].Value) != 16 {
		return nil, fmt.Errorf("host memory stall map: expected one 16-byte value")
	}
	host := decodeMemoryLatency(items[0].Value)
	data := []*metric.Data{
		metric.NewGaugeData("compaction_stall", float64(host.CompactionStall)/1e6, "time stalled in memory compaction", nil),
		metric.NewGaugeData("allocpages_stall", float64(host.AllocPagesStall)/1e6, "time stalled in alloc pages", nil),
	}
	if !obj.containers {
		return data, nil
	}
	containers, err := discover()
	if err != nil {
		return data, fmt.Errorf("discover memory stall containers: %w", err)
	}
	if len(containers) == 0 {
		return data, nil
	}
	items, err = obj.DumpMapByName("mm_container_free_compact_map")
	if err != nil {
		return data, fmt.Errorf("dump container memory stall map: %w", err)
	}
	if len(items) == 0 {
		return data, nil
	}
	byID := make(map[uint64]*pod.Container, len(containers))
	var resolveErr error
	for _, container := range containers {
		if container == nil {
			continue
		}
		root := filepath.Clean(container.CgroupPath)
		if !filepath.IsAbs(root) || root == "/" {
			continue
		}
		cgroupPath := paths.Path(root)
		if cgroups.CgroupMode() != cgroups.Unified {
			cgroupPath = paths.Path(subsystem.SubsystemMemory, root)
		}
		id, err := paths.KernfsID(cgroupPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			resolveErr = errors.Join(resolveErr, fmt.Errorf("resolve memory cgroup ID for %q: %w", cgroupPath, err))
			continue
		}
		if id == 0 {
			continue
		}
		byID[id] = container
	}
	containerData, err := containerMemoryStalls(items, byID)
	return append(data, containerData...), errors.Join(resolveErr, err)
}

func decodeMemoryLatency(value []byte) memoryLatency {
	return memoryLatency{
		CompactionStall: binary.LittleEndian.Uint64(value[:8]),
		AllocPagesStall: binary.LittleEndian.Uint64(value[8:]),
	}
}

func containerMemoryStalls(items []bpf.MapItem, containers map[uint64]*pod.Container) ([]*metric.Data, error) {
	data := make([]*metric.Data, 0, 2*len(containers))
	var decodeErr error
	for _, item := range items {
		if len(item.Key) != 8 || len(item.Value) != 16 {
			decodeErr = fmt.Errorf("container memory stall map: expected 8-byte key and 16-byte value")
			continue
		}
		id := binary.LittleEndian.Uint64(item.Key)
		container := containers[id]
		if id == 0 || container == nil {
			continue
		}
		mm := decodeMemoryLatency(item.Value)
		data = append(data,
			metric.NewContainerGaugeData(container, "compaction_stall", float64(mm.CompactionStall)/1e6, "cumulative milliseconds stalled in memory compaction", nil),
			metric.NewContainerGaugeData(container, "allocpages_stall", float64(mm.AllocPagesStall)/1e6, "cumulative milliseconds stalled in global direct reclaim", nil))
	}
	return data, decodeErr
}

// Start detect work, load bpf and wait data
func (c *reclaimCompact) Start(ctx context.Context) (retErr error) {
	obj, err := loadMemoryStalls(bpf.ThisBpfOBJ(), loadMemoryStallObject)
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
	enabled := uint32(1)
	if !containers {
		enabled = 0
	}
	return bpf.LoadBPFFromCollectionSpec(name, spec, map[string]any{"enable_container_stalls": enabled})
}

func loadMemoryStalls(name string, load func(string, bool) (bpf.BPF, error)) (*memoryStallObject, error) {
	var containerErr error
	for _, containers := range []bool{true, false} {
		obj, err := load(name, containers)
		if err == nil {
			opts := memoryStallAttachOptions()
			err = obj.AttachWithOptions(opts)
			if err == nil {
				if !containers {
					log.WithError(containerErr).Warn("container memory stalls unavailable; collecting host stalls only")
				}
				return &memoryStallObject{BPF: obj, containers: containers}, nil
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

func memoryStallAttachOptions() []bpf.AttachOption {
	// Attach completion probes before recording operation starts.
	return []bpf.AttachOption{
		{ProgramName: "tracepoint_try_to_free_pages_end", Symbol: "vmscan/mm_vmscan_direct_reclaim_end"},
		{ProgramName: "kretprobe_try_to_compact_pages_host", Symbol: "try_to_compact_pages"},
		{ProgramName: "tracepoint_try_to_free_pages_begin", Symbol: "vmscan/mm_vmscan_direct_reclaim_begin"},
		{ProgramName: "kprobe_try_to_compact_pages_host", Symbol: "try_to_compact_pages"},
	}
}
