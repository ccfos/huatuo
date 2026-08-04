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
	"encoding/binary"
	"errors"
	"math"
	"testing"
	"unsafe"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/bpf/abi"
	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/pkg/profiling"

	"github.com/cilium/ebpf"
	"github.com/stretchr/testify/require"
)

func TestOffCPUEventABI(t *testing.T) {
	var event abi.ProfilerOffCPUEvent
	require.Equal(t, uintptr(64), unsafe.Sizeof(event))
	require.Equal(t, uintptr(40), unsafe.Offsetof(event.StartNS))
	require.Equal(t, uintptr(48), unsafe.Offsetof(event.EndNS))
	require.Equal(t, uintptr(56), unsafe.Offsetof(event.CPU))
	require.Equal(t, uintptr(60), unsafe.Offsetof(event.Kind))
	require.Equal(t, uintptr(62), unsafe.Offsetof(event.Flags))
}

func TestOffCPUCategory(t *testing.T) {
	tests := []struct {
		kind  uint16
		flags uint16
		want  string
	}{
		{offCPUEventBlocked, 0, "off-CPU blocked"},
		{offCPUEventBlocked, offCPUFlagMissedWakeup, "off-CPU blocked (wakeup not observed)"},
		{offCPUEventRunqueue, 0, "scheduling delay"},
		{offCPUEventRunqueue, offCPUFlagMissedWakeup, "scheduling delay (wakeup not observed)"},
		{offCPUEventRunqueue, offCPUFlagPreempted, "scheduling delay (preempted)"},
		{offCPUEventRunqueue, offCPUFlagYielded, "scheduling delay (yielded)"},
		{99, 0, "off-CPU unknown"},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, offCPUCategory(tt.kind, tt.flags))
	}
}

type stackLookupMissBPF struct {
	bpf.BPF
}

func (stackLookupMissBPF) ReadMap(uint32, []byte) ([]byte, error) {
	return nil, ebpf.ErrKeyNotExist
}

func TestAggregateOffCPUBatch(t *testing.T) {
	event := func(pid uint32, value int64, kind uint16, kernelStackID, userStackID int32) any {
		return &abi.ProfilerOffCPUEvent{
			Base: abi.ProfilerEventBase{
				PIDTGID:   uint64(pid) << 32,
				Value:     value,
				Kernstack: kernelStackID,
				Userstack: userStackID,
			},
			Kind: kind,
		}
	}

	batch := []any{
		event(100, 10, offCPUEventBlocked, 0, -1),
		event(100, 20, offCPUEventBlocked, 0, -1),
		event(100, 30, offCPUEventRunqueue, 0, -1),
		event(200, 40, offCPUEventBlocked, 0, -1),
		event(300, 50, offCPUEventBlocked, -1, -1),
	}

	ringCtx := &ringBufferContext{bpf: stackLookupMissBPF{}, stackMapAID: 1}
	var samples []*stackSample
	ringCtx.aggregateOffCPUBatch(batch, func(record any) {
		samples = append(samples, record.(*stackSample))
	})

	require.Len(t, samples, 3)
	type sampleKey struct {
		PID      uint32
		Category string
	}
	values := make(map[sampleKey]int64)
	for _, sample := range samples {
		values[sampleKey{sample.Process.PID, sample.Category}] = sample.Value
	}
	require.Equal(t, int64(30), values[sampleKey{100, "off-CPU blocked"}])
	require.Equal(t, int64(30), values[sampleKey{100, "scheduling delay"}])
	require.Equal(t, int64(40), values[sampleKey{200, "off-CPU blocked"}])
}

func TestNativeAggregatorSeparatesOffCPUCategories(t *testing.T) {
	aggr := &nativeAggregator{aggrMap: make(map[string]*stackSample)}
	proc := processKey{PID: 123, Comm: "worker"}
	aggr.Aggregate(&stackSample{Process: proc, UserStack: "main;wait;", Value: 10, Category: "off-CPU blocked"})
	aggr.Aggregate(&stackSample{Process: proc, UserStack: "main;wait;", Value: 20, Category: "scheduling delay"})
	require.Len(t, aggr.aggrMap, 2)
}

func TestNativeOffCPUBPFConstants(t *testing.T) {
	pctx := &pcontext.ProfilerContext{
		PIDs:                []int{123},
		CPUIDs:              []int{2, 4},
		ThreadGroup:         true,
		OffCPUPhase:         profiling.OffCPUPhaseRunqueue,
		OffCPUMinDurationUS: 250,
		OffCPUStatsEnabled:  true,
	}
	constants := newNativeOffCPUBPFConstants(pctx, 456)
	require.Equal(t, uint32(123), constants["profiler_filter_pid"])
	require.Equal(t, uint64(456), constants["profiler_filter_css"])
	require.Equal(t, true, constants["profiler_filter_threads"])
	require.Equal(t, uint32(2), constants["profiler_offcpu_phase"])
	require.Equal(t, uint64(250000), constants["profiler_offcpu_min_duration_ns"])
	require.Equal(t, uint32(1), constants["profiler_offcpu_cpu_set_enabled"])
	require.Equal(t, uint32(1), constants["profiler_offcpu_stats_enabled"])
}

func TestNativeOffCPUBPFConstantsDisablesEmptyCPUSet(t *testing.T) {
	constants := newNativeOffCPUBPFConstants(&pcontext.ProfilerContext{}, 0)
	require.Equal(t, uint32(0), constants["profiler_offcpu_cpu_set_enabled"])
	require.Equal(t, uint32(0), constants["profiler_offcpu_stats_enabled"])
}

func TestOffCPUStatNames(t *testing.T) {
	require.Equal(t, []string{
		"stack_error",
		"state_error",
		"output_error",
		"missed_wakeup",
		"exit_cleanup",
	}, offCPUStatNames)
}

func TestOffCPUCPUSetItems(t *testing.T) {
	items, err := offCPUCPUSetItems([]int{0, 1, 63, 64, 127})
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.Equal(t, uint32(0), binary.LittleEndian.Uint32(items[0].Key))
	require.Equal(t, uint64(1<<63|3), binary.LittleEndian.Uint64(items[0].Value))
	require.Equal(t, uint32(1), binary.LittleEndian.Uint32(items[1].Key))
	require.Equal(t, uint64(1<<63|1), binary.LittleEndian.Uint64(items[1].Value))

	_, err = offCPUCPUSetItems([]int{offCPUCPUSetWordBits * offCPUCPUSetWordCount})
	require.EqualError(t, err, "cpuid 8192 exceeds off-CPU filter limit 8191")
}

type offCPUCPUSetBPF struct {
	bpf.BPF
	mapID      uint32
	writeErr   error
	writtenMap uint32
	items      []bpf.MapItem
}

func (f *offCPUCPUSetBPF) MapIDByName(string) uint32 {
	return f.mapID
}

func (f *offCPUCPUSetBPF) WriteMapItems(mapID uint32, items []bpf.MapItem) error {
	f.writtenMap = mapID
	f.items = items
	return f.writeErr
}

func TestConfigureOffCPUSet(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		obj := &offCPUCPUSetBPF{}
		require.NoError(t, configureOffCPUSet(obj, nil))
		require.Zero(t, obj.writtenMap)
	})

	t.Run("missing map", func(t *testing.T) {
		obj := &offCPUCPUSetBPF{}
		err := configureOffCPUSet(obj, []int{1})
		require.EqualError(t, err, `BPF map "offcpu_cpu_set" not found`)
	})

	t.Run("write failure", func(t *testing.T) {
		obj := &offCPUCPUSetBPF{mapID: 7, writeErr: errors.New("update failed")}
		err := configureOffCPUSet(obj, []int{1})
		require.EqualError(t, err, `write BPF map "offcpu_cpu_set": update failed`)
	})

	t.Run("configured", func(t *testing.T) {
		obj := &offCPUCPUSetBPF{mapID: 7}
		require.NoError(t, configureOffCPUSet(obj, []int{1, 65}))
		require.Equal(t, uint32(7), obj.writtenMap)
		require.Len(t, obj.items, 2)
	})
}

func TestMicrosecondsToNanosecondsSaturates(t *testing.T) {
	require.Equal(t, uint64(1000), microsecondsToNanoseconds(1))
	require.Equal(t, uint64(math.MaxUint64), microsecondsToNanoseconds(math.MaxUint64))
}

func TestNativeCPUOffCPUAttachOptions(t *testing.T) {
	opts := nativeOffCPUAttachOptions()
	require.Len(t, opts, 5)
	require.Equal(t, "native_offcpu_switch", opts[0].ProgramName)
	require.Equal(t, "sched_switch", opts[0].Symbol)
	require.Equal(t, "native_offcpu_free", opts[4].ProgramName)
	require.Equal(t, "sched_process_free", opts[4].Symbol)
}

func TestOffCPUProfileTypeUsesNanosecondsWithoutSampleRate(t *testing.T) {
	pctx := &pcontext.ProfilerContext{Type: profiling.TypeCPU, CPUMode: profiling.CPUModeOffCPU, Freq: 99}
	opt, profileType, err := profileTypeOptions(pctx)
	require.NoError(t, err)
	require.Zero(t, opt.SampleRate)
	require.Equal(t, "process_offcpu:offcpu:nanoseconds:offcpu:nanoseconds", profileType)
}
