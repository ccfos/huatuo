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
	"sync/atomic"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/utils/bytesutil"
	"huatuo-bamai/pkg/tracing"
)

func init() {
	tracing.RegisterEventTracing("iolatency", newIolatency)
}

func newIolatency() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &iolatencyTracing{},
		Interval:    10,
		Flag:        tracing.FlagTracing | tracing.FlagMetric,
	}, nil
}

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/iolatency_tracing.c -o $BPF_DIR/iolatency_tracing.o

const (
	blkContainerLatencyMap = "blkcg_map"
	blkDiskLatencyMap      = "blkdisk_map"
	blkLatencyZone         = 6
)

// BlkDiskEntry stores disk latency histogram buckets and freeze counts.
type BlkDiskEntry struct {
	Disk     uint64
	Major    uint32
	Minor    uint32
	FreezeNr uint64
	Q2CZone  [blkLatencyZone]uint64
	D2CZone  [blkLatencyZone]uint64
}

// BlkgqEntry stores cgroup latency histogram buckets
type BlkgqEntry struct {
	Blkgq   uint64
	Disk    uint64
	Major   uint32
	Minor   uint32
	Q2CZone [blkLatencyZone]uint64
	D2CZone [blkLatencyZone]uint64
}

type iolatencyTracing struct {
	running          atomic.Bool
	latestContainers map[string]*pod.Container
	bpfObject        bpf.BPF
}

func (c *iolatencyTracing) Start(ctx context.Context) error {
	b, err := bpf.LoadBpf(bpf.ThisBpfOBJ(), nil)
	if err != nil {
		return fmt.Errorf("failed to load bpf: %w", err)
	}
	defer b.Close()

	// commit 1e1a9cecfab3 ("block: force noio scope in blk_mq_freeze_queue")
	// made blk_mq_freeze_queue a static inline wrapper in v6.15; the
	// attachable symbol is blk_mq_freeze_queue_nomemsave. The first arg
	// (struct request_queue *) is unchanged, so BPF logic stays the same.
	freezeQueueSym := "blk_mq_freeze_queue"
	if !bpf.HasKprobeFunction(freezeQueueSym) {
		freezeQueueSym = "blk_mq_freeze_queue_nomemsave"
	}

	if err := b.AttachWithOptions([]bpf.AttachOption{
		{ProgramName: "kprobe_start_request", Symbol: "blk_mq_start_request"},
		{ProgramName: "kprobe_done_bio", Symbol: "__rq_qos_done_bio"},
		{ProgramName: "kprobe_freeze_queue", Symbol: freezeQueueSym},
	}); err != nil {
		return err
	}

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	b.WaitDetachByBreaker(childCtx, cancel)

	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	c.bpfObject = b
	c.running.Store(true)
	defer c.running.Store(false)

	for {
		select {
		case <-childCtx.Done():
			return nil
		case <-ticker.C:
			if err := c.updateContainerBlkDisk(b); err != nil {
				return err
			}
		}
	}
}

func (c *iolatencyTracing) dumpBlkdiskLatency() ([]BlkDiskEntry, error) {
	var latencyData []BlkDiskEntry

	disks, err := c.bpfObject.DumpMapByName(blkDiskLatencyMap)
	if err != nil {
		return nil, err
	}

	for _, disk := range disks {
		var info BlkDiskEntry

		buf := bytes.NewReader(disk.Value)
		if err := binary.Read(buf, binary.LittleEndian, &info); err != nil {
			return nil, err
		}

		latencyData = append(latencyData, info)
	}

	return latencyData, nil
}

func (c *iolatencyTracing) dumpContainerLatency() ([]BlkgqEntry, error) {
	var latencyData []BlkgqEntry

	containersData, err := c.bpfObject.DumpMapByName(blkContainerLatencyMap)
	if err != nil {
		return nil, err
	}

	for _, data := range containersData {
		var blkcg BlkgqEntry

		buf := bytes.NewReader(data.Value)
		if err := binary.Read(buf, binary.LittleEndian, &blkcg); err != nil {
			return nil, err
		}

		latencyData = append(latencyData, blkcg)
	}

	return latencyData, nil
}

func (c *iolatencyTracing) updateContainerBlkDisk(b bpf.BPF) error {
	containers, err := pod.Containers()
	if err != nil {
		return err
	}

	var newContainers []*pod.Container

	for id, container := range containers {
		if _, exists := c.latestContainers[id]; !exists {
			newContainers = append(newContainers, container)
		} else {
			delete(c.latestContainers, id)
		}
	}

	// delete the containers which may be deleted.
	var deletedContainersKeys [][]byte

	for _, container := range c.latestContainers {
		if blkcg, ok := container.CgroupCss[pod.SubSysBlkIO]; ok {
			deletedContainersKeys = append(deletedContainersKeys,
				bytesutil.ToBytes(blkcg))
		}
	}

	mapId := b.MapIDByName(blkContainerLatencyMap)
	if len(deletedContainersKeys) > 0 {
		if err := b.DeleteMapItems(mapId, deletedContainersKeys); err != nil {
			return err
		}
	}

	var items []bpf.MapItem
	for _, container := range newContainers {
		blkcg, ok := container.CgroupCss[pod.SubSysBlkIO]
		if !ok {
			continue
		}

		entry := &BlkgqEntry{Blkgq: blkcg}
		items = append(items, bpf.MapItem{
			Key:   bytesutil.ToBytes(blkcg),
			Value: bytesutil.ToBytes(entry),
		})
	}

	if len(items) > 0 {
		if err := b.WriteMapItems(mapId, items); err != nil {
			return err
		}
	}

	c.latestContainers = containers
	return nil
}
