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

	"github.com/stretchr/testify/require"
)

func TestRWLockAttachOptions(t *testing.T) {
	oldTracepoints := hasLockContentionTracepoints
	oldKprobe := hasRWLockKprobeFunction
	t.Cleanup(func() {
		hasLockContentionTracepoints = oldTracepoints
		hasRWLockKprobeFunction = oldKprobe
	})

	hasLockContentionTracepoints = func() bool { return true }
	hasRWLockKprobeFunction = func(string) bool { return false }
	options, backend, err := rwlockAttachOptions()
	require.NoError(t, err)
	require.Equal(t, rwlockBackendContentionTracepoints, backend)
	require.Len(t, options, 2)
	require.Equal(t, "trace_rwlock_contention_begin", options[0].ProgramName)

	hasLockContentionTracepoints = func() bool { return false }
	hasRWLockKprobeFunction = func(string) bool { return true }
	options, backend, err = rwlockAttachOptions()
	require.NoError(t, err)
	require.Equal(t, rwlockBackendQueuedSlowpaths, backend)
	require.Len(t, options, 4)
	require.Equal(t, rwlockReadSlowpathSymbol, options[0].Symbol)
	require.Equal(t, rwlockWriteSlowpathSymbol, options[2].Symbol)

	hasRWLockKprobeFunction = func(symbol string) bool {
		return symbol == rwlockReadSlowpathSymbol
	}
	_, _, err = rwlockAttachOptions()
	require.EqualError(
		t,
		err,
		"kernel exposes neither lock contention tracepoints nor all "+
			"queued rwlock slowpaths; missing [queued_write_lock_slowpath]",
	)
}

func TestAggregateRWLockBatchSeparatesAccess(t *testing.T) {
	base := lockContentionEvent{
		ProfilerEventBase: ProfilerEventBase{
			PidTgid:   uint64(42) << 32,
			Comm:      [TaskCommLen]byte{'a', 'p', 'p'},
			Kernstack: 0,
			Userstack: 1,
			Value:     15,
		},
		Lock:   0xab,
		Access: 1,
	}
	write := base
	write.Access = 2

	aggregates := make(
		map[lockContentionAggregateKey]lockContentionAggregateValue,
	)
	aggregateLockContentionBatch(aggregates, []any{&base, &write})

	require.Len(t, aggregates, 2)
	require.Equal(t, "read", lockAccessName(1))
	require.Equal(t, "write", lockAccessName(2))
	require.Empty(t, lockAccessName(0))
}
