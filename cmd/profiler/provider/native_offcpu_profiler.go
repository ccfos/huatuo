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
	"huatuo-bamai/internal/profiler/procutil"
	"huatuo-bamai/pkg/profiling"
	"huatuo-bamai/pkg/types"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/native_offcpu_profiler.c -o $BPF_DIR/native_offcpu_profiler.o

const (
	offCPUEventBlocked  uint16 = 1
	offCPUEventRunqueue uint16 = 2

	offCPUFlagPreempted    uint16 = 1 << 0
	offCPUFlagYielded      uint16 = 1 << 1
	offCPUFlagMissedWakeup uint16 = 1 << 2
)

type offCPUStackKey struct {
	Stack    stackIDPair
	Category string
}

func nativeOffCPUAttachOptions() []bpf.AttachOption {
	return []bpf.AttachOption{
		{ProgramName: "native_cpu_offcpu_switch", Symbol: "sched_switch"},
		{ProgramName: "native_cpu_offcpu_wakeup", Symbol: "sched_wakeup"},
		{ProgramName: "native_cpu_offcpu_wakeup_new", Symbol: "sched_wakeup_new"},
		{ProgramName: "native_cpu_offcpu_exit", Symbol: "sched_process_exit"},
		{ProgramName: "native_cpu_offcpu_free", Symbol: "sched_process_free"},
	}
}

func newNativeOffCPUBPFConstants(pctx *pcontext.ProfilerContext, cssAddr uint64) map[string]any {
	constants := newNativeBPFConstants(pctx.PID(), cssAddr, pctx.ThreadGroup)
	constants["profiler_offcpu_metric"] = offCPUMetricCode(pctx.OffCPUMetric)
	constants["profiler_offcpu_min_ns"] = microsecondsToNanoseconds(pctx.OffCPUMinUS)
	constants["profiler_offcpu_max_ns"] = microsecondsToNanoseconds(pctx.OffCPUMaxUS)
	return constants
}

func offCPUMetricCode(metric profiling.OffCPUMetric) uint32 {
	switch metric {
	case profiling.OffCPUMetricBlocked:
		return 1
	case profiling.OffCPUMetricRunnable:
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
			var lostErr *bpf.PerfEventSamplesLostError
			if errors.As(err, &lostErr) {
				log.Warnf("off-CPU perf event samples lost: %d", lostErr.Count)
			}

			readErr := perfEventReadErrorWithoutLoss(err)
			if errors.Is(readErr, types.ErrExitByCancelCtx) {
				return nil
			}
			if readErr != nil {
				return fmt.Errorf("read off-CPU events: %w", readErr)
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

func perfEventReadErrorWithoutLoss(err error) error {
	if err == nil {
		return nil
	}

	type multiUnwrapper interface {
		Unwrap() []error
	}
	if joined, ok := err.(multiUnwrapper); ok {
		var remaining []error
		for _, nestedErr := range joined.Unwrap() {
			if !errors.Is(nestedErr, bpf.ErrPerfEventSamplesLost) {
				remaining = append(remaining, nestedErr)
			}
		}
		return errors.Join(remaining...)
	}

	if errors.Is(err, bpf.ErrPerfEventSamplesLost) {
		return nil
	}
	return err
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
			Comm: procutil.CommToString(event.Base.Comm),
		}
		stack := offCPUStackKey{
			Category: offCPUCategory(event.Kind, event.Flags),
			Stack: stackIDPair{
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
				Process:     process,
				UserStack:   r.resolveUserStack(r.stackMapAID, stack.Stack.UserStackID, process.PID),
				KernelStack: r.resolveKernelStack(r.stackMapAID, stack.Stack.KernelStackID),
				Value:       duration,
				Category:    stack.Category,
			})
		}
	}
}

func offCPUCategory(kind, flags uint16) string {
	switch kind {
	case offCPUEventBlocked:
		if flags&offCPUFlagMissedWakeup != 0 {
			return "off-CPU blocked (wakeup not observed)"
		}
		return "off-CPU blocked"
	case offCPUEventRunqueue:
		if flags&offCPUFlagMissedWakeup != 0 {
			return "scheduling delay (wakeup not observed)"
		}
		if flags&offCPUFlagPreempted != 0 {
			return "scheduling delay (preempted)"
		}
		if flags&offCPUFlagYielded != 0 {
			return "scheduling delay (yielded)"
		}
		return "scheduling delay"
	default:
		return "off-CPU unknown"
	}
}

var offCPUStatNames = []string{
	"tracked",
	"blocked_emitted",
	"runqueue_emitted",
	"below_threshold",
	"above_threshold",
	"stack_error",
	"state_error",
	"output_error",
	"missed_wakeup",
	"exit_cleanup",
}

func logNativeOffCPUStats(obj bpf.BPF) {
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
