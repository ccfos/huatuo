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

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/bpf/abi"
	"huatuo-bamai/internal/log"
	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/pkg/profiling"
	"huatuo-bamai/pkg/types"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/native_offcpu_profiler.c -o $BPF_DIR/native_offcpu_profiler.o

const (
	offCPUCPUSetMapName   = "offcpu_cpu_set"
	offCPUCPUSetWordBits  = 64
	offCPUCPUSetWordCount = 128
)

type offCPUStackKey struct {
	Stack    rawStackIDs
	Category string
}

func nativeOffCPUAttachOptions() []bpf.AttachOption {
	return []bpf.AttachOption{
		{ProgramName: "native_offcpu_switch", Symbol: "sched_switch"},
		{ProgramName: "native_offcpu_wakeup", Symbol: "sched_wakeup"},
		{ProgramName: "native_offcpu_wakeup_new", Symbol: "sched_wakeup_new"},
		{ProgramName: "native_offcpu_exit", Symbol: "sched_process_exit"},
		{ProgramName: "native_offcpu_free", Symbol: "sched_process_free"},
	}
}

func newNativeOffCPUBPFConstants(pctx *pcontext.ProfilerContext, cssAddr uint64) map[string]any {
	constants := newNativeBPFConstants(pctx.PID(), cssAddr, pctx.ThreadGroup)
	constants["profiler_offcpu_phase"] = offCPUPhaseCode(pctx.OffCPUPhase)
	constants["profiler_offcpu_min_duration_ns"] = microsecondsToNanoseconds(pctx.OffCPUMinDurationUS)
	constants["profiler_offcpu_cpu_set_enabled"] = uint32(0)
	constants["profiler_offcpu_stats_enabled"] = uint32(0)
	if len(pctx.CPUIDs) != 0 {
		constants["profiler_offcpu_cpu_set_enabled"] = uint32(1)
	}
	if pctx.OffCPUStatsEnabled {
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
		items = append(items, bpf.MapItem{Key: key, Value: value})
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
	enqueue func(any),
) error {
	ringCtx, err := newSingleRingBufferContext(p.bpf, ctx, 4096*257)
	if err != nil {
		return err
	}
	defer ringCtx.Close()

	for {
		batch, err := ringCtx.readerA.ReadBatch(func() any { return &abi.ProfilerOffCPUEvent{} })
		ringCtx.aggregateOffCPUBatch(batch, enqueue)

		if err != nil {
			if errors.Is(err, types.ErrExitByCancelCtx) {
				return nil
			}

			var lostErr *bpf.PerfEventSamplesLostError
			if !errors.As(err, &lostErr) {
				return fmt.Errorf("read off-CPU perf event batch: %w", err)
			}

			log.Warnf("off-CPU perf event samples lost: %d", lostErr.Count)
			// ReadBatch joins sample loss with read or decode failures.
			var joinedErr interface{ Unwrap() []error }
			if errors.As(err, &joinedErr) {
				return fmt.Errorf("read off-CPU perf event batch: %w", err)
			}
		}

		if len(batch) == 0 {
			select {
			case <-ctx.Done():
				return nil
			default:
				continue
			}
		}
	}
}

func (r *ringBufferContext) aggregateOffCPUBatch(batch []any, enqueue func(any)) {
	countsByProcess := make(map[processKey]map[offCPUStackKey]int64)
	for _, record := range batch {
		event, ok := record.(*abi.ProfilerOffCPUEvent)
		if !ok {
			log.Warnf("unexpected off-CPU event type %T", record)
			continue
		}
		if event.Base.Value <= 0 {
			continue
		}
		if !validateStackID(event.Base.Kernstack) && !validateStackID(event.Base.Userstack) {
			continue
		}

		process := processKey{
			PID:  uint32(event.Base.PIDTGID >> 32),
			Comm: taskCommString(event.Base.Comm),
		}
		stack := offCPUStackKey{
			Category: offCPUCategory(event.Kind),
			Stack: rawStackIDs{
				KernelStackID: event.Base.Kernstack,
				UserStackID:   event.Base.Userstack,
			},
		}
		if countsByProcess[process] == nil {
			countsByProcess[process] = make(map[offCPUStackKey]int64)
		}
		countsByProcess[process][stack] += event.Base.Value
	}

	for process, stacks := range countsByProcess {
		for stack, duration := range stacks {
			enqueue(&stackSample{
				Process: process,
				StackTrace: symbolizedStackTrace{
					UserFrames: r.resolveUserStack(
						r.stackMapAID,
						stack.Stack.UserStackID,
						process.PID,
					),
					KernelFrames: r.resolveKernelStack(
						r.stackMapAID,
						stack.Stack.KernelStackID,
					),
				},
				Value:    duration,
				Category: stack.Category,
			})
		}
	}
}

var offCPUCategories = [...]string{
	abi.ProfilerOffCPUEventUnknown:              "off-CPU unknown",
	abi.ProfilerOffCPUEventBlocked:              "off-CPU blocked",
	abi.ProfilerOffCPUEventRunqueue:             "scheduling delay",
	abi.ProfilerOffCPUEventRunqueuePreempted:    "scheduling delay (preempted)",
	abi.ProfilerOffCPUEventRunqueueYielded:      "scheduling delay (yielded)",
	abi.ProfilerOffCPUEventRunqueueMissedWakeup: "scheduling delay (wakeup not observed)",
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
