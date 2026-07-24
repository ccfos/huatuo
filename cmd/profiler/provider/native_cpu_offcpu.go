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

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler/bpfmap"
	"huatuo-bamai/internal/profiler/procutil"
	"huatuo-bamai/internal/symbol"
	"huatuo-bamai/pkg/types"
)

const (
	offCPUEventABIVersion uint16 = 1

	offCPUEventBlocked  uint8 = 1
	offCPUEventRunqueue uint8 = 2

	offCPUFlagPreempted    uint8 = 1 << 0
	offCPUFlagYielded      uint8 = 1 << 1
	offCPUFlagMissedWakeup uint8 = 1 << 2
)

type nativeOffCPUReader struct {
	bpf        bpf.BPF
	reader     bpf.PerfEventReader
	stackMapID uint32
	usym       *symbol.UsymResolver
}

func newNativeOffCPUReader(obj bpf.BPF, ctx context.Context) (*nativeOffCPUReader, error) {
	reader, err := obj.EventPipeByName(ctx, "offcpu_output", 4096*257)
	if err != nil {
		return nil, err
	}

	stackMapID := obj.MapIDByName("offcpu_stack_map")
	if stackMapID == 0 {
		_ = reader.Close()
		return nil, fmt.Errorf("offcpu_stack_map not found")
	}

	return &nativeOffCPUReader{
		bpf:        obj,
		reader:     reader,
		stackMapID: stackMapID,
		usym:       symbol.NewUsymResolver(),
	}, nil
}

func (r *nativeOffCPUReader) Close() error {
	if r == nil || r.reader == nil {
		return nil
	}
	err := r.reader.Close()
	r.reader = nil
	return err
}

type offCPUStackKey struct {
	Process  processIDName
	Stack    bpfmap.StackTraceID
	Category string
}

func (p *cpuNativeProfiler) readOffCPUDataLoop(ctx context.Context, enqueue func(any)) error {
	log.Infof("off-CPU data reading loop started")
	defer func() {
		lost := uint64(0)
		if p.offCPUReader != nil && p.offCPUReader.reader != nil {
			lost = p.offCPUReader.reader.LostSamples()
		}
		log.Infof("off-CPU data reading loop ended: lost_samples=%d", lost)
	}()

	stopDbg, err := p.dbg.StartDebugEventLoop(ctx, p.bpf, "dbg_native_cpu_offcpu_dbg_events")
	if err != nil {
		return fmt.Errorf("start off-CPU bpf debug loop: %w", err)
	}
	defer stopDbg()

	if p.offCPUReader == nil {
		return fmt.Errorf("off-CPU event reader is not initialized")
	}

	for {
		batch, err := p.offCPUReader.reader.ReadBatch(&offCPUEventKey{})
		if err != nil {
			if errors.Is(err, types.ErrExitByCancelCtx) {
				return nil
			}
			return fmt.Errorf("read off-CPU events: %w", err)
		}
		if len(batch) == 0 {
			select {
			case <-ctx.Done():
				return nil
			default:
				continue
			}
		}

		p.offCPUReader.aggregateBatch(batch, enqueue)
	}
}

func (r *nativeOffCPUReader) aggregateBatch(batch []any, enqueue func(any)) {
	counts := make(map[offCPUStackKey]int64)
	for _, record := range batch {
		event, ok := record.(*offCPUEventKey)
		if !ok {
			log.Warnf("unexpected off-CPU event type %T", record)
			continue
		}
		if event.ABIVersion != offCPUEventABIVersion {
			log.Warnf("unsupported off-CPU event ABI %d", event.ABIVersion)
			continue
		}
		if event.Value <= 0 {
			continue
		}

		key := offCPUStackKey{
			Process: processIDName{
				Pid:  uint32(event.PidTgid >> 32),
				Name: procutil.CommToString(event.Comm),
			},
			Category: offCPUCategory(event.Kind, event.Flags),
			Stack: bpfmap.StackTraceID{
				KernelID: event.Kernstack,
				UserID:   event.Userstack,
			},
		}
		counts[key] += event.Value
	}

	for key, duration := range counts {
		enqueue(&stackEntry{
			Proc: &processIDName{
				Pid:  key.Process.Pid,
				Name: key.Process.Name,
			},
			User:     r.resolveUserStack(key.Stack.UserID, key.Process.Pid),
			Kernel:   r.resolveKernelStack(key.Stack.KernelID),
			Samples:  duration,
			Category: key.Category,
		})
	}
}

func offCPUCategory(kind, flags uint8) string {
	switch kind {
	case offCPUEventBlocked:
		if flags&offCPUFlagMissedWakeup != 0 {
			return "off-CPU blocked (wakeup not observed)"
		}
		return "off-CPU blocked"
	case offCPUEventRunqueue:
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

// Stack ID zero is valid. Only negative IDs indicate bpf_get_stackid failure.
func (r *nativeOffCPUReader) resolveKernelStack(stackID int32) string {
	if !validOffCPUStackID(stackID) {
		return ""
	}
	trace, ok := readStackTrace(r.bpf, r.stackMapID, stackID)
	if !ok {
		return ""
	}
	return strings.Join(symbol.KsymStackStrsReversed(trace[:], len(trace)), ";") + ";"
}

func (r *nativeOffCPUReader) resolveUserStack(stackID int32, pid uint32) string {
	if !validOffCPUStackID(stackID) {
		return ""
	}
	trace, ok := readStackTrace(r.bpf, r.stackMapID, stackID)
	if !ok {
		return ""
	}
	return strings.Join(r.usym.UsymStackStrsReversed(pid, trace[:], len(trace)), ";") + ";"
}

func validOffCPUStackID(stackID int32) bool {
	return stackID >= 0
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
