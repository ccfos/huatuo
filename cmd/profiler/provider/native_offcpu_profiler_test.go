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
	"math"
	"testing"
	"unsafe"

	"github.com/ccfos/huatuo/internal/profiling/aggregation"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/profiling/pipeline"
	"github.com/ccfos/huatuo/pkg/profiling"
	"github.com/ccfos/huatuo/pkg/types"

	"github.com/cilium/ebpf"
	"github.com/stretchr/testify/require"
)

func TestOffCPUEventABI(t *testing.T) {
	var event abi.ProfilerOffCPUEvent
	require.Equal(t, uintptr(48), unsafe.Sizeof(event))
	require.Equal(t, uintptr(40), unsafe.Offsetof(event.Kind))
}

func TestOffCPUCategory(t *testing.T) {
	tests := []struct {
		kind abi.ProfilerOffCPUEventKind
		want string
	}{
		{
			kind: abi.ProfilerOffCPUEventUnknown,
			want: "unknown",
		},
		{
			kind: abi.ProfilerOffCPUEventBlocked,
			want: "blocked",
		},
		{
			kind: abi.ProfilerOffCPUEventRunqueue,
			want: "runqueue",
		},
		{
			kind: abi.ProfilerOffCPUEventRunqueuePreempted,
			want: "runqueue_preempted",
		},
		{
			kind: abi.ProfilerOffCPUEventRunqueueYielded,
			want: "runqueue_yielded",
		},
		{
			kind: abi.ProfilerOffCPUEventRunqueueMissedWakeup,
			want: "runqueue_missed_wakeup",
		},
		{
			kind: 99,
			want: "unknown",
		},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, offCPUCategory(tt.kind))
	}
}

type stackLookupMissBPF struct {
	bpf.BPF
}

func (stackLookupMissBPF) ReadMap(uint32, []byte) ([]byte, error) {
	return nil, ebpf.ErrKeyNotExist
}

type offCPUReaderStub struct {
	readBatch func() (bpf.PerfEventBatch, error)
}

func (*offCPUReaderStub) ReadInto(any) error {
	return nil
}

func (r *offCPUReaderStub) ReadBatch(func() any) (bpf.PerfEventBatch, error) {
	return r.readBatch()
}

func (*offCPUReaderStub) Close() error {
	return nil
}

func newOffCPUReaderProfiler(reader bpf.PerfEventReader) *cpuNativeProfiler {
	obj := stackLookupMissBPF{}
	return &cpuNativeProfiler{
		bpf:        obj,
		offCPUMode: true,
		ringCtx: &ringBufferContext{
			bpf:         obj,
			readerA:     reader,
			stackMapAID: 1,
		},
	}
}

func TestReadOffCPUDataLoopReturnsReaderErrors(t *testing.T) {
	readErr := errors.New("read failed")
	tests := []struct {
		name  string
		batch bpf.PerfEventBatch
	}{
		{name: "read error"},
		{
			name: "read error with lost samples",
			batch: bpf.PerfEventBatch{
				LostSamples: 7,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			calls := 0
			reader := &offCPUReaderStub{readBatch: func() (bpf.PerfEventBatch, error) {
				calls++
				if calls == 2 {
					cancel()
				}

				return tt.batch, readErr
			}}
			profiler := newOffCPUReaderProfiler(reader)

			err := profiler.ReadDataLoop(ctx, func(*pipeline.SampleBatch) error { return nil })
			require.ErrorIs(t, err, readErr)
			require.Equal(t, 1, calls)
		})
	}
}

func TestReadOffCPUDataLoopContinuesAfterSamplesLost(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	calls := 0
	reader := &offCPUReaderStub{readBatch: func() (bpf.PerfEventBatch, error) {
		calls++
		if calls == 2 {
			cancel()
		}

		return bpf.PerfEventBatch{LostSamples: 7}, nil
	}}
	profiler := newOffCPUReaderProfiler(reader)

	require.NoError(t, profiler.readOffCPUDataLoop(ctx, func(*pipeline.SampleBatch) error { return nil }))
	require.Equal(t, 2, calls)
}

func TestReadOffCPUDataLoopTreatsCancellationAsCleanShutdown(t *testing.T) {
	tests := []struct {
		name        string
		lostSamples uint64
	}{
		{name: "canceled"},
		{
			name:        "canceled after samples lost",
			lostSamples: 7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			reader := &offCPUReaderStub{readBatch: func() (bpf.PerfEventBatch, error) {
				calls++
				return bpf.PerfEventBatch{LostSamples: tt.lostSamples}, types.ErrExitByCancelCtx
			}}
			profiler := newOffCPUReaderProfiler(reader)

			require.NoError(
				t,
				profiler.readOffCPUDataLoop(t.Context(), func(*pipeline.SampleBatch) error { return nil }),
			)
			require.Equal(t, 1, calls)
		})
	}
}

func TestReadOffCPUDataLoopPreservesPartialBatchOnError(t *testing.T) {
	readErr := errors.New("read failed")
	event := &abi.ProfilerOffCPUEvent{
		Base: abi.ProfilerEventBase{
			PIDTGID:   uint64(100) << 32,
			Value:     10,
			Kernstack: 0,
			Userstack: -1,
		},
		Kind: abi.ProfilerOffCPUEventBlocked,
	}
	reader := &offCPUReaderStub{readBatch: func() (bpf.PerfEventBatch, error) {
		return bpf.PerfEventBatch{Events: []any{event}}, readErr
	}}
	profiler := newOffCPUReaderProfiler(reader)

	var got []aggregation.Sample
	err := profiler.readOffCPUDataLoop(
		t.Context(),
		func(batch *pipeline.SampleBatch) error { got = append(got, batch.Samples...); return nil },
	)
	require.ErrorIs(t, err, readErr)
	require.Len(t, got, 1)
}

func TestEnqueueOffCPUBatch(t *testing.T) {
	event := func(
		pid uint32,
		value int64,
		kind abi.ProfilerOffCPUEventKind,
		kernelStackID,
		userStackID int32,
	) any {
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
		event(
			100,
			10,
			abi.ProfilerOffCPUEventBlocked,
			0,
			-1,
		),
		event(
			100,
			20,
			abi.ProfilerOffCPUEventBlocked,
			0,
			-1,
		),
		event(
			100,
			30,
			abi.ProfilerOffCPUEventRunqueue,
			0,
			-1,
		),
		event(
			200,
			40,
			abi.ProfilerOffCPUEventBlocked,
			0,
			-1,
		),
		event(
			300,
			50,
			abi.ProfilerOffCPUEventBlocked,
			-1,
			-1,
		),
	}

	ringCtx := &ringBufferContext{
		bpf:         stackLookupMissBPF{},
		stackMapAID: 1,
	}
	var samples []aggregation.Sample
	require.NoError(
		t,
		ringCtx.enqueueOffCPUBatch(
			batch,
			func(batch *pipeline.SampleBatch) error { samples = append(samples, batch.Samples...); return nil },
		),
	)
	require.Len(t, samples, 5)
	type sampleKey struct{ PID, Category string }
	values := make(map[sampleKey]int64)
	for i := range samples {
		sample := &samples[i]
		var key sampleKey
		for _, label := range sample.Labels {
			if label.Name == "pid" {
				key.PID = label.StringValue
			}

			if label.Name == "offcpu_category" {
				key.Category = label.StringValue
			}
		}

		values[key] += sample.Value
	}

	require.Equal(t, int64(30), values[sampleKey{
		PID:      "100",
		Category: "blocked",
	}])
	require.Equal(t, int64(30), values[sampleKey{
		PID:      "100",
		Category: "runqueue",
	}])
	require.Equal(t, int64(40), values[sampleKey{
		PID:      "200",
		Category: "blocked",
	}])
}

func TestNativeOffCPUBPFConstants(t *testing.T) {
	cfg := &Config{
		PIDs:                []int{123},
		CPUIDs:              []int{2, 4},
		ThreadGroup:         true,
		OffCPUPhase:         profiling.OffCPUPhaseRunqueue,
		OffCPUMinDurationUS: 250,
		OffCPUStatsEnabled:  true,
	}
	constants := newNativeOffCPUBPFConstants(cfg, 456)
	require.Equal(t, uint32(123), constants["profiler_filter_pid"])
	require.Equal(t, uint64(456), constants["profiler_filter_css"])
	require.Equal(t, true, constants["profiler_filter_threads"])
	require.Equal(t, uint32(2), constants["profiler_offcpu_phase"])
	require.Equal(t, uint64(250000), constants["profiler_offcpu_min_duration_ns"])
	require.Equal(t, uint32(1), constants["profiler_offcpu_cpu_set_enabled"])
	require.Equal(t, uint32(1), constants["profiler_offcpu_stats_enabled"])
}

func TestNativeOffCPUBPFConstantsDisablesEmptyCPUSet(t *testing.T) {
	constants := newNativeOffCPUBPFConstants(&Config{}, 0)
	require.Equal(t, uint32(0), constants["profiler_offcpu_cpu_set_enabled"])
	require.Equal(t, uint32(0), constants["profiler_offcpu_stats_enabled"])
}

func TestOffCPUStatNames(t *testing.T) {
	require.Equal(t, [abi.ProfilerOffCPUStatMax]string{
		abi.ProfilerOffCPUStatStackFailure:       "stack_failure",
		abi.ProfilerOffCPUStatStateUpdateFailure: "state_update_failure",
		abi.ProfilerOffCPUStatOutputFailure:      "output_failure",
		abi.ProfilerOffCPUStatMissedWakeup:       "missed_wakeup",
		abi.ProfilerOffCPUStatStateCleanup:       "state_cleanup",
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
		obj := &offCPUCPUSetBPF{
			mapID:    7,
			writeErr: errors.New("update failed"),
		}
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
	cfg := &Config{
		Type: profiling.TypeCPU,
		Mode: profiling.ModeOffCPU,
		Freq: 99,
	}
	profileType, err := sampleType(cfg)
	require.NoError(t, err)
	require.Equal(t, "process_offcpu:offcpu:nanoseconds:offcpu:nanoseconds", profileType)
}
