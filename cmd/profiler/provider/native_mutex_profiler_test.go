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

func TestValidateLockTarget(t *testing.T) {
	require.NoError(t, validateLockTarget(&pcontext.ProfilerContext{
		PIDs: []int{42},
	}))
	require.NoError(t, validateLockTarget(&pcontext.ProfilerContext{
		ContainerID: "container",
	}))
	require.EqualError(
		t,
		validateLockTarget(&pcontext.ProfilerContext{}),
		"native lock profiler requires exactly one PID or container target",
	)
	require.EqualError(
		t,
		validateLockTarget(&pcontext.ProfilerContext{
			PIDs:        []int{42},
			ContainerID: "container",
		}),
		"native lock profiler requires exactly one PID or container target",
	)
}

func TestMutexAttachOptions(t *testing.T) {
	oldTracepoints := hasLockContentionTracepoints
	oldKprobe := hasMutexKprobeFunction
	t.Cleanup(func() {
		hasLockContentionTracepoints = oldTracepoints
		hasMutexKprobeFunction = oldKprobe
	})

	hasLockContentionTracepoints = func() bool { return true }
	hasMutexKprobeFunction = func(string) bool { return false }
	options, backend, err := mutexAttachOptions()
	require.NoError(t, err)
	require.Equal(t, mutexBackendContentionTracepoints, backend)
	require.Len(t, options, 2)
	require.Equal(t, "trace_mutex_contention_begin", options[0].ProgramName)

	hasLockContentionTracepoints = func() bool { return false }
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

func TestSpinlockAttachOptions(t *testing.T) {
	oldTracepoints := hasLockContentionTracepoints
	t.Cleanup(func() {
		hasLockContentionTracepoints = oldTracepoints
	})

	hasLockContentionTracepoints = func() bool { return true }
	options, backend, err := spinlockAttachOptions()
	require.NoError(t, err)
	require.Equal(t, spinlockBackendTracepoints, backend)
	require.Len(t, options, 2)
	require.Equal(t, "trace_spin_contention_begin", options[0].ProgramName)
	require.Equal(t, "trace_spin_contention_end", options[1].ProgramName)

	hasLockContentionTracepoints = func() bool { return false }
	_, _, err = spinlockAttachOptions()
	require.EqualError(
		t,
		err,
		"spinlock contention requires lock:contention_begin/end "+
			"tracepoints (Linux 5.19+); refusing unsafe "+
			"spinlock slowpath probes",
	)
}

func TestAggregateLockContentionBatch(t *testing.T) {
	comm := [TaskCommLen]byte{'a', 'p', 'p'}
	first := &lockContentionEvent{
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

	aggregates := make(
		map[lockContentionAggregateKey]lockContentionAggregateValue,
	)
	aggregateLockContentionBatch(aggregates, []any{
		first,
		&second,
		&lockContentionEvent{ProfilerEventBase: ProfilerEventBase{Value: 0}},
		"unexpected",
	})

	require.Equal(t, map[lockContentionAggregateKey]lockContentionAggregateValue{
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
