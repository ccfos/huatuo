// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package provider

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ccfos/huatuo/internal/profiling/aggregation"

	"github.com/ccfos/huatuo/internal/profiling/pipeline"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/pkg/profiling"
	"github.com/ccfos/huatuo/pkg/types"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/native_offcpu_profiler.c -o $BPF_DIR/native_offcpu_profiler.o

const (
	offCPUCPUSetMapName   = "offcpu_cpu_set"
	offCPUCPUSetWordBits  = 64
	offCPUCPUSetWordCount = 128
)

func nativeOffCPUAttachOptions() []bpf.AttachOption {
	return []bpf.AttachOption{
		{
			ProgramName: "native_offcpu_switch",
			Symbol:      "sched_switch",
		},
		{
			ProgramName: "native_offcpu_wakeup",
			Symbol:      "sched_wakeup",
		},
		{
			ProgramName: "native_offcpu_wakeup_new",
			Symbol:      "sched_wakeup_new",
		},
		{
			ProgramName: "native_offcpu_exit",
			Symbol:      "sched_process_exit",
		},
		{
			ProgramName: "native_offcpu_free",
			Symbol:      "sched_process_free",
		},
	}
}

func newNativeOffCPUBPFConstants(cfg *Config, cssAddr uint64) map[string]any {
	constants := newNativeBPFConstants(cfg.PID(), cssAddr, cfg.ThreadGroup)
	constants["profiler_offcpu_phase"] = offCPUPhaseCode(cfg.OffCPUPhase)
	constants["profiler_offcpu_min_duration_ns"] = microsecondsToNanoseconds(cfg.OffCPUMinDurationUS)
	constants["profiler_offcpu_cpu_set_enabled"] = uint32(0)
	constants["profiler_offcpu_stats_enabled"] = uint32(0)
	if len(cfg.CPUIDs) != 0 {
		constants["profiler_offcpu_cpu_set_enabled"] = uint32(1)
	}

	if cfg.OffCPUStatsEnabled {
		constants["profiler_offcpu_stats_enabled"] = uint32(1)
	}

	return constants
}

func configureOffCPUSet(obj bpf.BPF, cpuIDs []int) error {
	if len(cpuIDs) == 0 {
		return nil
	}

	mapID := obj.MapIDByName(offCPUCPUSetMapName)
	if mapID == 0 {
		return fmt.Errorf("BPF map %q not found", offCPUCPUSetMapName)
	}

	items, err := offCPUCPUSetItems(cpuIDs)
	if err != nil {
		return err
	}

	if err := obj.WriteMapItems(mapID, items); err != nil {
		return fmt.Errorf("write BPF map %q: %w", offCPUCPUSetMapName, err)
	}

	return nil
}

func offCPUCPUSetItems(cpuIDs []int) ([]bpf.MapItem, error) {
	var masks [offCPUCPUSetWordCount]uint64
	for _, cpuID := range cpuIDs {
		if cpuID < 0 || cpuID >= offCPUCPUSetWordBits*offCPUCPUSetWordCount {
			return nil, fmt.Errorf("cpuid %d exceeds off-CPU filter limit %d",
				cpuID, offCPUCPUSetWordBits*offCPUCPUSetWordCount-1)
		}

		word := cpuID / offCPUCPUSetWordBits
		masks[word] |= uint64(1) << uint(cpuID%offCPUCPUSetWordBits)
	}

	items := make([]bpf.MapItem, 0, len(cpuIDs))
	for word, mask := range &masks {
		if mask == 0 {
			continue
		}

		key := make([]byte, 4)
		binary.LittleEndian.PutUint32(key, uint32(word))
		value := make([]byte, 8)
		binary.LittleEndian.PutUint64(value, mask)
		items = append(items, bpf.MapItem{
			Key:   key,
			Value: value,
		})
	}

	return items, nil
}

func offCPUPhaseCode(phase profiling.OffCPUPhase) uint32 {
	switch phase {
	case profiling.OffCPUPhaseBlocked:
		return 1
	case profiling.OffCPUPhaseRunqueue:
		return 2
	default:
		return 0
	}
}

func microsecondsToNanoseconds(value uint64) uint64 {
	const nsPerMicrosecond = uint64(time.Microsecond)
	if value > ^uint64(0)/nsPerMicrosecond {
		return ^uint64(0)
	}

	return value * nsPerMicrosecond
}

func (p *cpuNativeProfiler) readOffCPUDataLoop(
	ctx context.Context,
	enqueue func(*pipeline.SampleBatch) error,
) error {
	ringCtx := p.ringCtx

	for {
		batch, err := ringCtx.readerA.ReadBatch(func() any { return &abi.ProfilerOffCPUEvent{} })
		if err := ringCtx.enqueueOffCPUBatch(batch.Events, enqueue); err != nil {
			return err
		}

		if batch.LostSamples != 0 {
			if err := enqueue(&pipeline.SampleBatch{
				DroppedSamples: map[string]uint64{types.DropReasonCollection: batch.LostSamples},
			}); err != nil {
				return err
			}
		}

		if err != nil {
			if errors.Is(err, types.ErrExitByCancelCtx) {
				return nil
			}

			return fmt.Errorf("read off-CPU perf event batch: %w", err)
		}

		if len(batch.Events) == 0 {
			select {
			case <-ctx.Done():
				return nil
			default:
				continue
			}
		}
	}
}

func (r *ringBufferContext) enqueueOffCPUBatch(events []any, enqueue func(*pipeline.SampleBatch) error) error {
	// Reuse immutable frames within this batch; later batches resolve anew.
	userCache := make(map[userStackCacheKey][]types.ProfileFrame)
	kernelCache := make(map[int32][]types.ProfileFrame)
	var builder pipeline.BatchBuilder
	for _, record := range events {
		event, ok := record.(*abi.ProfilerOffCPUEvent)
		if !ok {
			return fmt.Errorf("unexpected off-CPU event %T", record)
		}

		pid := uint32(event.Base.PIDTGID >> 32)
		labels := append(
			nativeLabels(pid, taskCommString(event.Base.Comm)),
			types.ProfileStringLabel("offcpu_category", offCPUCategory(event.Kind)),
		)
		labels, err := types.NormalizeLabels(labels)
		if err != nil {
			return err
		}

		// Missing stacks remain retryable while the BPF map is live.
		userKey := userStackCacheKey{
			PID:     pid,
			StackID: event.Base.Userstack,
		}
		user := userCache[userKey]
		if user == nil {
			user = r.resolveUserStack(r.stackMapAID, event.Base.Userstack, pid)
			if len(user) != 0 {
				userCache[userKey] = user
			}
		}

		kernel := kernelCache[event.Base.Kernstack]
		if kernel == nil {
			kernel = r.resolveKernelStack(r.stackMapAID, event.Base.Kernstack)
			if len(kernel) != 0 {
				kernelCache[event.Base.Kernstack] = kernel
			}
		}

		sample := aggregation.Sample{
			UserFrames:   user,
			KernelFrames: kernel,
			Labels:       labels,
			Value:        event.Base.Value,
		}
		if err := builder.Append(&sample, enqueue); err != nil {
			return err
		}
	}

	return builder.Flush(enqueue)
}

var offCPUCategories = [...]string{
	abi.ProfilerOffCPUEventUnknown:              "unknown",
	abi.ProfilerOffCPUEventBlocked:              "blocked",
	abi.ProfilerOffCPUEventRunqueue:             "runqueue",
	abi.ProfilerOffCPUEventRunqueuePreempted:    "runqueue_preempted",
	abi.ProfilerOffCPUEventRunqueueYielded:      "runqueue_yielded",
	abi.ProfilerOffCPUEventRunqueueMissedWakeup: "runqueue_missed_wakeup",
}

func offCPUCategory(kind abi.ProfilerOffCPUEventKind) string {
	index := uint64(kind)
	if index >= uint64(len(offCPUCategories)) {
		return offCPUCategories[abi.ProfilerOffCPUEventUnknown]
	}

	return offCPUCategories[index]
}

var offCPUStatNames = [abi.ProfilerOffCPUStatMax]string{
	abi.ProfilerOffCPUStatStackFailure:       "stack_failure",
	abi.ProfilerOffCPUStatStateUpdateFailure: "state_update_failure",
	abi.ProfilerOffCPUStatOutputFailure:      "output_failure",
	abi.ProfilerOffCPUStatMissedWakeup:       "missed_wakeup",
	abi.ProfilerOffCPUStatStateCleanup:       "state_cleanup",
}

func logOffCPUBPFStats(obj bpf.BPF) {
	if obj == nil {
		return
	}

	mapID := obj.MapIDByName("offcpu_stats")
	if mapID == 0 {
		return
	}

	stats := make([]string, 0, len(offCPUStatNames))
	for index, name := range offCPUStatNames {
		key := make([]byte, 4)
		binary.LittleEndian.PutUint32(key, uint32(index))
		value, err := obj.ReadMap(mapID, key)
		if err != nil {
			log.Warnf("read off-CPU stat %s: %v", name, err)
			continue
		}

		var total uint64
		for offset := 0; offset+8 <= len(value); offset += 8 {
			total += binary.LittleEndian.Uint64(value[offset : offset+8])
		}

		stats = append(stats, fmt.Sprintf("%s=%d", name, total))
	}

	log.Infof("off-CPU stats: %s", strings.Join(stats, " "))
}
