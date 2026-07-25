// Copyright 2026 The HuaTuo Authors
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

package provider

import (
	"testing"

	pcontext "huatuo-bamai/internal/profiler/context"

	"github.com/stretchr/testify/require"
)

func TestValidateMutexTarget(t *testing.T) {
	require.NoError(t, validateMutexTarget(&pcontext.ProfilerContext{
		PIDs: []int{42},
	}))
	require.NoError(t, validateMutexTarget(&pcontext.ProfilerContext{
		ContainerID: "container",
	}))
	require.EqualError(
		t,
		validateMutexTarget(&pcontext.ProfilerContext{}),
		"native lock profiler requires exactly one PID or container target",
	)
	require.EqualError(
		t,
		validateMutexTarget(&pcontext.ProfilerContext{
			PIDs:        []int{42},
			ContainerID: "container",
		}),
		"native lock profiler requires exactly one PID or container target",
	)
}

func TestMutexAttachOptions(t *testing.T) {
	oldTracepoints := hasMutexContentionTracepoints
	oldKprobe := hasMutexKprobeFunction
	t.Cleanup(func() {
		hasMutexContentionTracepoints = oldTracepoints
		hasMutexKprobeFunction = oldKprobe
	})

	hasMutexContentionTracepoints = func() bool { return true }
	hasMutexKprobeFunction = func(string) bool { return false }
	options, backend, err := mutexAttachOptions()
	require.NoError(t, err)
	require.Equal(t, mutexBackendContentionTracepoints, backend)
	require.Len(t, options, 2)
	require.Equal(t, "trace_mutex_contention_begin", options[0].ProgramName)

	hasMutexContentionTracepoints = func() bool { return false }
	hasMutexKprobeFunction = func(symbol string) bool {
		return symbol == mutexSlowpathSymbol
	}
	options, backend, err = mutexAttachOptions()
	require.NoError(t, err)
	require.Equal(t, mutexBackendSlowpathKprobe, backend)
	require.Len(t, options, 2)
	require.Equal(t, mutexSlowpathSymbol, options[0].Symbol)

	hasMutexKprobeFunction = func(string) bool { return false }
	_, _, err = mutexAttachOptions()
	require.EqualError(
		t,
		err,
		"kernel exposes neither lock contention tracepoints nor "+
			mutexSlowpathSymbol,
	)
}

func TestAggregateMutexBatch(t *testing.T) {
	comm := [TaskCommLen]byte{'a', 'p', 'p'}
	first := &mutexEvent{
		ProfilerEventBase: ProfilerEventBase{
			PidTgid:   uint64(42) << 32,
			Comm:      comm,
			Kernstack: 0,
			Userstack: 1,
			Value:     15,
		},
		Lock: 0xab,
	}
	second := *first
	second.Value = 5

	aggregates := make(map[mutexAggregateKey]mutexAggregateValue)
	aggregateMutexBatch(aggregates, []any{
		first,
		&second,
		&mutexEvent{ProfilerEventBase: ProfilerEventBase{Value: 0}},
		"unexpected",
	})

	require.Equal(t, map[mutexAggregateKey]mutexAggregateValue{
		{
			Pid:       42,
			Comm:      comm,
			Kernstack: 0,
			Userstack: 1,
			Lock:      0xab,
		}: {
			WaitTime:  20,
			Contended: 2,
		},
	}, aggregates)
}
