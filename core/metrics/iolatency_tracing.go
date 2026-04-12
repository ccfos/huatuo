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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pod"
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

type iolatencyTracing struct {
	running atomic.Bool

	dataLock             sync.RWMutex
	diskLatencyData      []DiskEntry
	containerLatencyData []BlkgqEntry
	blkcgContainerMap    map[uint64]*pod.Container

	oldContainerMap map[string]*pod.Container
}

func (e *BlkgqEntry) structToSlice() []byte {
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, e)
	return buf.Bytes()
}

// Start loads the iolatency BPF object and refreshes the histogram snapshots.
func (c *iolatencyTracing) Start(ctx context.Context) error {
	b, err := bpf.LoadBpf(bpf.ThisBpfOBJ(), nil)
	if err != nil {
		return fmt.Errorf("failed to load bpf: %w", err)
	}
	defer b.Close()

	if err := attachIOLatencyPrograms(b); err != nil {
		return err
	}

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	b.WaitDetachByBreaker(childCtx, cancel)

	c.oldContainerMap = make(map[string]*pod.Container)
	c.blkcgContainerMap = make(map[uint64]*pod.Container)

	log.Infof("start iolatency")

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	c.running.Store(true)
	defer c.running.Store(false)

	for {
		select {
		case <-childCtx.Done():
			return nil
		case <-ticker.C:
			if err := c.updateBlkgqInfo(b); err != nil {
				return err
			}
			if err := c.readDiskLatencyData(b); err != nil {
				return err
			}
			if err := c.readContainerLatencyData(b); err != nil {
				return err
			}
		}
	}
}

func attachIOLatencyPrograms(b bpf.BPF) error {
	info, err := b.Info()
	if err != nil {
		return err
	}

	attachOptions := make([]bpf.AttachOption, 0, len(info.ProgramsInfo))
	for _, progInfo := range info.ProgramsInfo {
		parts := strings.SplitN(progInfo.SectionName, "/", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid section name: %s", progInfo.SectionName)
		}

		attachOptions = append(attachOptions, bpf.AttachOption{
			ProgramName: progInfo.Name,
			Symbol:      parts[1],
		})
	}

	return b.AttachWithOptions(attachOptions)
}

func (c *iolatencyTracing) readDiskLatencyData(b bpf.BPF) error {
	data, err := b.DumpMapByName("disk_info")
	if err != nil {
		return err
	}

	newDiskLatencyData := make([]DiskEntry, 0, len(data))
	for _, ioData := range data {
		var diskInfo DiskEntry

		buf := bytes.NewReader(ioData.Value)
		if err := binary.Read(buf, binary.LittleEndian, &diskInfo); err != nil {
			return err
		}

		newDiskLatencyData = append(newDiskLatencyData, diskInfo)
	}

	c.dataLock.Lock()
	c.diskLatencyData = newDiskLatencyData
	c.dataLock.Unlock()

	return nil
}

func (c *iolatencyTracing) readContainerLatencyData(b bpf.BPF) error {
	data, err := b.DumpMapByName("blkgq_info")
	if err != nil {
		return err
	}

	newContainerLatencyData := make([]BlkgqEntry, 0, len(data))
	for _, ioData := range data {
		var blkcg BlkgqEntry

		buf := bytes.NewReader(ioData.Value)
		if err := binary.Read(buf, binary.LittleEndian, &blkcg); err != nil {
			return err
		}

		newContainerLatencyData = append(newContainerLatencyData, blkcg)
	}

	c.dataLock.Lock()
	c.containerLatencyData = newContainerLatencyData
	c.dataLock.Unlock()

	return nil
}

func (c *iolatencyTracing) updateBlkgqInfo(b bpf.BPF) error {
	mapID := b.MapIDByName("blkgq_info")

	currContainers, err := pod.Containers()
	if err != nil {
		log.Warnf("get all containers error: %v", err)
		return nil
	}

	newBlkcgContainerMap := make(map[uint64]*pod.Container, len(currContainers))
	newPods := make([]*pod.Container, 0)

	for containerID, container := range currContainers {
		if _, exists := c.oldContainerMap[containerID]; !exists {
			newPods = append(newPods, container)
		} else {
			delete(c.oldContainerMap, containerID)
		}

		if blkcg, ok := container.CgroupCss[pod.SubSysBlkIO]; ok {
			newBlkcgContainerMap[blkcg] = container
		}
	}

	deletedPods := make([][]byte, 0, len(c.oldContainerMap))
	for _, container := range c.oldContainerMap {
		if blkcg, ok := container.CgroupCss[pod.SubSysBlkIO]; ok {
			deletedPods = append(deletedPods, uint64ToBytes(blkcg))
		}
	}
	if len(deletedPods) > 0 {
		if err := b.DeleteMapItems(mapID, deletedPods); err != nil {
			return err
		}
	}

	items := make([]bpf.MapItem, 0, len(newPods))
	for _, container := range newPods {
		blkcg, ok := container.CgroupCss[pod.SubSysBlkIO]
		if !ok {
			continue
		}

		entry := &BlkgqEntry{Blkgq: blkcg}
		items = append(items, bpf.MapItem{
			Key:   uint64ToBytes(blkcg),
			Value: entry.structToSlice(),
		})
	}
	if len(items) > 0 {
		if err := b.WriteMapItems(mapID, items); err != nil {
			return err
		}
	}

	c.dataLock.Lock()
	c.blkcgContainerMap = newBlkcgContainerMap
	c.dataLock.Unlock()

	c.oldContainerMap = currContainers
	return nil
}

func uint64ToBytes(n uint64) []byte {
	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.LittleEndian, n); err != nil {
		log.Warnf("binary.Write failed: %v", err)
		return nil
	}
	return buf.Bytes()
}
