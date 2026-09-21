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
	"context"
	"errors"
	"fmt"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	"os"
	"sync"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/internal/utils/bytesutil"
	"github.com/ccfos/huatuo/pkg/types"
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
	blkDiskBucketMap       = "blkdisk_lat_map"
	blkContainerBucketMap  = "blkcg_lat_map"
	ioLatencyStatusMap     = "io_latency_status_map"
	ioLatencyQueueProbeMap = "io_latency_queue_probe"
	ioLatencyDiskEvents    = "io_latency_disk_events"
	ioLatencyBucketCount   = 17
	ioLatencyStageCount    = 3
	ioSizeBucketCount      = 6
)

// BlkDiskEntry stores disk identity and its freeze count.
type BlkDiskEntry struct {
	Disk     uint64
	Major    uint32
	Minor    uint32
	FreezeNr uint64
}

// BlkgqEntry stores the disk binding for a tracked blkio cgroup.
type BlkgqEntry struct {
	Disk uint64
}

type ioLatencyHostKey struct {
	Major     uint32
	Minor     uint32
	Operation uint32
}

type ioLatencyContainerKey struct {
	Blkcg     uint64
	Major     uint32
	Minor     uint32
	Operation uint32
	Pad       uint32
}

type ioLatencyCounters struct {
	Buckets    [ioLatencyStageCount][ioLatencyBucketCount]uint64
	QueuedSize [ioSizeBucketCount]uint64
	IssuedSize [ioSizeBucketCount]uint64
}

type ioLatencyDiskCounters struct {
	Major    uint32
	Minor    uint32
	Retired  uint64
	Counters [2]ioLatencyCounters
}

type ioLatencySession struct {
	object           bpf.BPF
	previous         *ioLatencySnapshot
	latestContainers map[string]*pod.Container
	cancel           context.CancelCauseFunc
	retiredDisks     map[uint64]BlkDiskEntry
	diskChanges      uint64
	diskEvents       chan struct{}
	disksNeedScan    bool
	diskProbeName    [32]byte

	// Started retirements must finish even if a container ID reappears.
	pendingContainerCleanup [][]byte
}

type iolatencyTracing struct {
	mu      sync.Mutex
	session *ioLatencySession
}

func (c *iolatencyTracing) Start(ctx context.Context) (retErr error) {
	constants, statDisable, err := loadIOLatencyKernelConstants()
	if err != nil {
		log.Warnf("iolatency: startup kernel check: %v", err)
		return err
	}
	arguments, err := loadIOLatencyTracepointArguments()
	if err != nil {
		return err
	}
	for name, argument := range arguments {
		constants[name] = argument
	}
	constants["io_latency_containers_enabled"] = pod.ContainerSyncEnabled()
	b, err := bpf.LoadBPF(bpf.ThisBpfOBJ(), constants)
	if err != nil {
		return fmt.Errorf("failed to load bpf: %w", err)
	}
	defer b.Close()

	childCtx, cancel := context.WithCancelCause(ctx)
	defer func() {
		if cause := context.Cause(childCtx); errors.Is(cause, types.ErrTracingStopped) {
			retErr = cause
		}
		cancel(nil)
	}()
	session := &ioLatencySession{
		object:     b,
		cancel:     cancel,
		diskEvents: make(chan struct{}, 1),
	}
	reader, err := b.EventPipeByName(childCtx, ioLatencyDiskEvents,
		uint32(os.Getpagesize()))
	if err != nil {
		return fmt.Errorf("open iolatency disk notifications: %w", err)
	}
	diskReaderDone := make(chan struct{})
	go func() {
		defer close(diskReaderDone)
		session.readDiskEvents(childCtx, reader)
	}()
	defer func() {
		cancel(nil)
		reader.Close()
		<-diskReaderDone
	}()
	// Listen before attaching and scanning, so changes during enrollment
	// leave a notification for another pass.
	if err := attachIOLatencyHooks(b, statDisable); err != nil {
		return err
	}
	if err := session.refreshDisks(true); err != nil {
		log.Warnf("iolatency: startup queue check: %v", err)
		return err
	}

	c.mu.Lock()
	c.session = session
	c.mu.Unlock()
	defer c.withdrawSession()

	b.DetachOnContextDone(childCtx, func() { cancel(nil) })

	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-childCtx.Done():
			return nil
		case <-session.diskEvents:
			c.mu.Lock()
			err := session.refreshDisks(false)
			c.mu.Unlock()
			if err != nil {
				log.Warnf("iolatency: refresh disks: %v; retrying on the next notification", err)
			}
		case <-ticker.C:
			err := session.updateContainerBlkDisk(pod.SynchronizedContainers)
			if err != nil {
				// Restart if updating the BPF container maps fails.
				return err
			}
		}
	}
}

func (c *iolatencyTracing) withdrawSession() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.session = nil
}

func attachIOLatencyHooks(object bpf.BPF, statDisable bool) error {
	// The wrapper can be inlined while the nomemsave entry remains attachable.
	freezeQueueSym := "blk_mq_freeze_queue"
	if !bpf.HasKprobeFunction(freezeQueueSym) {
		freezeQueueSym = "blk_mq_freeze_queue_nomemsave"
	}
	options := []bpf.AttachOption{
		{
			ProgramName: "kprobe_disk_release",
			Symbol:      "disk_release",
		},
		{
			ProgramName: "kretprobe_register_queue",
			Symbol:      "blk_register_queue",
		},
		{
			ProgramName: "probe_queue_stats",
			Symbol:      "queue_attr_show",
		},
		{
			ProgramName: "kretprobe_queue_config",
			Symbol:      "queue_attr_store",
		},
		{
			ProgramName: "kprobe_unprep_clone",
			Symbol:      "blk_rq_unprep_clone",
		},
		{
			ProgramName: "kprobe_freeze_queue",
			Symbol:      freezeQueueSym,
		},
	}
	for _, symbol := range []string{
		"blk_stat_add_callback", "blk_stat_remove_callback", "blk_stat_enable_accounting",
	} {
		options = append(options, bpf.AttachOption{ProgramName: "kretprobe_stats_config", Symbol: symbol})
	}
	// Kernels with one-way accounting enablement have no disable function.
	if statDisable {
		options = append(options, bpf.AttachOption{
			ProgramName: "kretprobe_stats_config", Symbol: "blk_stat_disable_accounting",
		})
	}
	// Every path is required; a partial attachment is closed by Start before
	// a session can be published. Complete precedes the Q/A state producers.
	for _, definition := range ioLatencyTracepoints {
		options = append(options, bpf.AttachOption{
			ProgramName: definition.program,
			Symbol:      definition.symbol,
		})
	}
	if err := object.AttachWithOptions(options); err != nil {
		return fmt.Errorf("attach iolatency hooks: %w", err)
	}
	return nil
}

func (s *ioLatencySession) dumpBlkdiskLatency() ([]BlkDiskEntry, error) {
	var latencyData []BlkDiskEntry

	disks, err := s.object.DumpMapByName(blkDiskLatencyMap)
	if err != nil {
		return nil, err
	}

	for _, disk := range disks {
		var info BlkDiskEntry
		if err := decodeBPFMapData(disk.Value, &info); err != nil {
			return nil, fmt.Errorf("decode disk freeze counters: %w", err)
		}

		latencyData = append(latencyData, info)
	}

	return latencyData, nil
}

func (s *ioLatencySession) updateContainerBlkDisk(query func() (map[string]*pod.Container, error)) error {
	containers, available := ioControlQueryContainers(query)
	if !available {
		// Retain registrations until a successful query confirms deletions;
		// host tracing does not require an initial container catalog.
		return nil
	}

	var newContainers []*pod.Container

	for id, container := range containers {
		if _, exists := s.latestContainers[id]; !exists {
			newContainers = append(newContainers, container)
		}
	}

	for id, container := range s.latestContainers {
		if _, exists := containers[id]; exists {
			continue
		}
		if blkcg, ok := container.CgroupCss[subsystem.SubsystemBlkIO]; ok {
			s.pendingContainerCleanup = append(s.pendingContainerCleanup,
				bytesutil.ToBytes(blkcg))
		}
		delete(s.latestContainers, id)
	}

	mapId := s.object.MapIDByName(blkContainerLatencyMap)
	if len(s.pendingContainerCleanup) > 0 {
		if err := s.deleteContainerLatency(s.pendingContainerCleanup); err != nil {
			// No new admissions until retirement finishes, so pending cleanup
			// stays bounded by the previously registered container catalog.
			log.Warnf("iolatency: clean exited containers: %v; retrying", err)
			return nil
		}
		s.pendingContainerCleanup = nil
	}

	var items []bpf.MapItem
	for _, container := range newContainers {
		blkcg, ok := container.CgroupCss[subsystem.SubsystemBlkIO]
		if !ok {
			continue
		}

		entry := &BlkgqEntry{}
		items = append(items, bpf.MapItem{
			Key:   bytesutil.ToBytes(blkcg),
			Value: bytesutil.ToBytes(entry),
		})
	}

	if len(items) > 0 {
		if err := s.object.WriteMapItems(mapId, items); err != nil {
			return err
		}
	}

	s.latestContainers = containers
	return nil
}

func (s *ioLatencySession) deleteContainerLatency(deletedBlkcg [][]byte) error {
	b := s.object
	// Revoke admission before scanning counters. An earlier attempt may have
	// removed some or all keys; confirm absence and finish a partial batch.
	if err := s.deleteMapKeys(blkContainerLatencyMap, deletedBlkcg); err != nil {
		return err
	}
	deleted := make(map[string]struct{}, len(deletedBlkcg))
	for _, key := range deletedBlkcg {
		deleted[string(key)] = struct{}{}
	}

	series, err := b.DumpMapByName(blkContainerBucketMap)
	if err != nil {
		return err
	}

	var stale [][]byte
	for _, item := range series {
		if len(item.Key) < 8 {
			return fmt.Errorf("invalid iolatency container key size: %d",
				len(item.Key))
		}
		if _, ok := deleted[string(item.Key[:8])]; ok {
			stale = append(stale, item.Key)
		}
	}
	return s.deleteMapKeys(blkContainerBucketMap, stale)
}
