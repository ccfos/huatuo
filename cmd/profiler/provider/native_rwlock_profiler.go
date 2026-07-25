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
	"fmt"

	"huatuo-bamai/internal/bpf"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/native_rwlock_profiler.c -o $BPF_DIR/native_rwlock_profiler.o

const (
	rwlockBackendContentionTracepoints = "contention tracepoints"
	rwlockBackendQueuedSlowpaths       = "queued rwlock slowpaths"
	rwlockReadSlowpathSymbol           = "queued_read_lock_slowpath"
	rwlockWriteSlowpathSymbol          = "queued_write_lock_slowpath"
)

var hasRWLockKprobeFunction = bpf.HasKprobeFunction

func rwlockAttachOptions() ([]bpf.AttachOption, string, error) {
	if hasLockContentionTracepoints() {
		return []bpf.AttachOption{
			{
				ProgramName: "trace_rwlock_contention_begin",
				Symbol:      "lock/contention_begin",
			},
			{
				ProgramName: "trace_rwlock_contention_end",
				Symbol:      "lock/contention_end",
			},
		}, rwlockBackendContentionTracepoints, nil
	}

	missing := make([]string, 0, 2)
	for _, symbol := range []string{
		rwlockReadSlowpathSymbol,
		rwlockWriteSlowpathSymbol,
	} {
		if !hasRWLockKprobeFunction(symbol) {
			missing = append(missing, symbol)
		}
	}
	if len(missing) > 0 {
		return nil, "", fmt.Errorf(
			"kernel exposes neither lock contention tracepoints nor all "+
				"queued rwlock slowpaths; missing %v",
			missing,
		)
	}

	return []bpf.AttachOption{
		{
			ProgramName: "trace_rwlock_read_slowpath",
			Symbol:      rwlockReadSlowpathSymbol,
		},
		{
			ProgramName: "trace_rwlock_read_slowpath_return",
			Symbol:      rwlockReadSlowpathSymbol,
		},
		{
			ProgramName: "trace_rwlock_write_slowpath",
			Symbol:      rwlockWriteSlowpathSymbol,
		},
		{
			ProgramName: "trace_rwlock_write_slowpath_return",
			Symbol:      rwlockWriteSlowpathSymbol,
		},
	}, rwlockBackendQueuedSlowpaths, nil
}
