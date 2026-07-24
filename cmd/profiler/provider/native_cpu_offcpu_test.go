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
	"math"
	"testing"
	"unsafe"

	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/pkg/profiling"

	"github.com/stretchr/testify/require"
)

func TestOffCPUEventABI(t *testing.T) {
	var event offCPUEventKey
	require.Equal(t, uintptr(64), unsafe.Sizeof(event))
	require.Equal(t, uintptr(40), unsafe.Offsetof(event.StartNS))
	require.Equal(t, uintptr(48), unsafe.Offsetof(event.EndNS))
	require.Equal(t, uintptr(56), unsafe.Offsetof(event.CPU))
	require.Equal(t, uintptr(60), unsafe.Offsetof(event.ABIVersion))
	require.Equal(t, uintptr(62), unsafe.Offsetof(event.Kind))
	require.Equal(t, uintptr(63), unsafe.Offsetof(event.Flags))
}

func TestOffCPUCategory(t *testing.T) {
	tests := []struct {
		kind  uint8
		flags uint8
		want  string
	}{
		{offCPUEventBlocked, 0, "off-CPU blocked"},
		{offCPUEventBlocked, offCPUFlagMissedWakeup, "off-CPU blocked (wakeup not observed)"},
		{offCPUEventRunqueue, 0, "scheduling delay"},
		{offCPUEventRunqueue, offCPUFlagPreempted, "scheduling delay (preempted)"},
		{offCPUEventRunqueue, offCPUFlagYielded, "scheduling delay (yielded)"},
		{99, 0, "off-CPU unknown"},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, offCPUCategory(tt.kind, tt.flags))
	}
}

func TestValidOffCPUStackIDIncludesZero(t *testing.T) {
	require.True(t, validOffCPUStackID(0))
	require.True(t, validOffCPUStackID(1))
	require.False(t, validOffCPUStackID(-1))
}

func TestNativeAggregatorSeparatesOffCPUCategories(t *testing.T) {
	aggr := &nativeAggregator{aggrMap: make(map[string]*stackEntry)}
	proc := &processIDName{Pid: 123, Name: "worker"}
	aggr.Aggregate(&stackEntry{Proc: proc, User: "main;wait;", Samples: 10, Category: "off-CPU blocked"})
	aggr.Aggregate(&stackEntry{Proc: proc, User: "main;wait;", Samples: 20, Category: "scheduling delay"})
	require.Len(t, aggr.aggrMap, 2)
}

func TestNativeOffCPUBPFConstants(t *testing.T) {
	pctx := &pcontext.ProfilerContext{
		PIDs:         []int{123},
		ThreadGroup:  true,
		OffCPUMetric: profiling.OffCPUMetricRunnable,
		OffCPUMinUS:  250,
		OffCPUMaxUS:  5000,
	}
	constants := newNativeOffCPUBPFConstants(pctx, 456)
	require.Equal(t, uint32(123), constants["profiler_filter_pid"])
	require.Equal(t, uint64(456), constants["profiler_filter_css"])
	require.Equal(t, true, constants["profiler_filter_threads"])
	require.Equal(t, uint32(2), constants["profiler_offcpu_metric"])
	require.Equal(t, uint64(250000), constants["profiler_offcpu_min_ns"])
	require.Equal(t, uint64(5000000), constants["profiler_offcpu_max_ns"])
}

func TestMicrosecondsToNanosecondsSaturates(t *testing.T) {
	require.Equal(t, uint64(1000), microsecondsToNanoseconds(1))
	require.Equal(t, uint64(math.MaxUint64), microsecondsToNanoseconds(math.MaxUint64))
}

func TestNativeCPUOffCPUAttachOptions(t *testing.T) {
	opts := nativeCPUOffCPUAttachOptions()
	require.Len(t, opts, 5)
	require.Equal(t, "native_cpu_offcpu_switch", opts[0].ProgramName)
	require.Equal(t, "sched_switch", opts[0].Symbol)
	require.Equal(t, "native_cpu_offcpu_free", opts[4].ProgramName)
	require.Equal(t, "sched_process_free", opts[4].Symbol)
}

func TestOffCPUProfileTypeUsesNanosecondsWithoutSampleRate(t *testing.T) {
	pctx := &pcontext.ProfilerContext{Type: profiling.TypeCPU, CPUMode: profiling.CPUModeOffCPU, Freq: 99}
	opt, profileType, err := profileTypeOptions(pctx)
	require.NoError(t, err)
	require.Zero(t, opt.SampleRate)
	require.Equal(t, "process_offcpu:offcpu:nanoseconds:offcpu:nanoseconds", profileType)
}
