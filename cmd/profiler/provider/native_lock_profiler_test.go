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
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"huatuo-bamai/internal/bpf"
	pcontext "huatuo-bamai/internal/profiler/context"
)

func setLockProbeAvailability(t *testing.T, tracepoints bool, available map[string]bool) {
	t.Helper()
	oldTracepoints := hasLockContentionTracepoints
	oldKprobe := hasLockKprobeFunction
	hasLockContentionTracepoints = func() bool { return tracepoints }
	hasLockKprobeFunction = func(name string) bool { return available[name] }
	t.Cleanup(func() {
		hasLockContentionTracepoints = oldTracepoints
		hasLockKprobeFunction = oldKprobe
	})
}

func TestLockAttachOptionsPrefersContentionTracepoints(t *testing.T) {
	setLockProbeAvailability(t, true, nil)

	got, backend, err := lockAttachOptions([]string{"mutex", "spinlock", "rwlock"})
	if err != nil {
		t.Fatalf("lockAttachOptions() error = %v", err)
	}
	want := []bpf.AttachOption{
		{ProgramName: "trace_lock_contention_begin", Symbol: "lock/contention_begin"},
		{ProgramName: "trace_lock_contention_end", Symbol: "lock/contention_end"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lockAttachOptions() = %#v, want %#v", got, want)
	}
	if backend != lockBackendContentionTracepoint {
		t.Fatalf("backend = %q, want %q", backend, lockBackendContentionTracepoint)
	}
}

func TestLockAttachOptionsValidatesTypesBeforeUsingTracepoints(t *testing.T) {
	setLockProbeAvailability(t, true, nil)

	for _, lockTypes := range [][]string{nil, {"unknown"}} {
		if _, _, err := lockAttachOptions(lockTypes); err == nil {
			t.Fatalf("lockAttachOptions(%v) error = nil", lockTypes)
		}
	}
}

func TestLockAttachOptionsSupportsAllKernelLockTypesWithSlowpaths(t *testing.T) {
	available := map[string]bool{
		"__mutex_lock_slowpath":            true,
		"native_queued_spin_lock_slowpath": true,
		"queued_read_lock_slowpath":        true,
		"queued_write_lock_slowpath":       true,
	}
	setLockProbeAvailability(t, false, available)

	got, backend, err := lockAttachOptions([]string{"mutex", "spinlock", "rwlock"})
	if err != nil {
		t.Fatalf("lockAttachOptions() error = %v", err)
	}
	want := []bpf.AttachOption{
		{ProgramName: "trace_mutex_lock", Symbol: "__mutex_lock_slowpath"},
		{ProgramName: "trace_mutex_lock_return", Symbol: "__mutex_lock_slowpath"},
		{ProgramName: "trace_spin_lock", Symbol: "native_queued_spin_lock_slowpath"},
		{ProgramName: "trace_spin_lock_return", Symbol: "native_queued_spin_lock_slowpath"},
		{ProgramName: "trace_rw_lock", Symbol: "queued_read_lock_slowpath"},
		{ProgramName: "trace_rw_lock_return", Symbol: "queued_read_lock_slowpath"},
		{ProgramName: "trace_rw_lock", Symbol: "queued_write_lock_slowpath"},
		{ProgramName: "trace_rw_lock_return", Symbol: "queued_write_lock_slowpath"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lockAttachOptions() = %#v, want %#v", got, want)
	}
	if backend != lockBackendSlowpathKprobe {
		t.Fatalf("backend = %q, want %q", backend, lockBackendSlowpathKprobe)
	}
}

func TestLockAttachOptionsOnlyAttachesAvailableSlowpaths(t *testing.T) {
	available := map[string]bool{
		"__mutex_lock_slowpath":               true,
		"__mutex_lock_interruptible_slowpath": true,
		"__mutex_lock_killable_slowpath":      true,
		"native_queued_spin_lock_slowpath":    true,
		"__pv_queued_spin_lock_slowpath":      true,
	}
	setLockProbeAvailability(t, false, available)

	got, _, err := lockAttachOptions([]string{"mutex", "spinlock", "mutex"})
	if err != nil {
		t.Fatalf("lockAttachOptions() error = %v", err)
	}
	if gotLen, wantLen := len(got), 10; gotLen != wantLen {
		t.Fatalf("len(lockAttachOptions()) = %d, want %d: %#v", gotLen, wantLen, got)
	}
	for _, option := range got {
		if !strings.HasSuffix(option.Symbol, "slowpath") {
			t.Errorf("unsafe non-slowpath symbol attached: %q", option.Symbol)
		}
		if strings.HasPrefix(option.Symbol, "_raw_") {
			t.Errorf("raw lock fast path attached: %q", option.Symbol)
		}
	}
}

func TestLockAttachOptionsReportsUnavailableType(t *testing.T) {
	setLockProbeAvailability(t, false, nil)

	if _, _, err := lockAttachOptions([]string{"mutex"}); err == nil {
		t.Fatal("lockAttachOptions() error = nil")
	}
}

func TestLockAttachOptionsRequiresReadAndWriteRWLock(t *testing.T) {
	setLockProbeAvailability(t, false, map[string]bool{"queued_read_lock_slowpath": true})

	if _, _, err := lockAttachOptions([]string{"rwlock"}); err == nil {
		t.Fatal("lockAttachOptions() error = nil when write-side rwlock probe is unavailable")
	}
}

func TestLockTypesMask(t *testing.T) {
	if got, want := lockTypesMask([]string{"mutex", "rwlock"}), uint32(0b101); got != want {
		t.Fatalf("lockTypesMask() = %03b, want %03b", got, want)
	}
}

func TestLockStatBinaryLayout(t *testing.T) {
	var key lockStatKey
	if got, want := unsafe.Sizeof(key), uintptr(48); got != want {
		t.Fatalf("sizeof(lockStatKey) = %d, want %d", got, want)
	}
	var value lockStatValue
	if got, want := unsafe.Sizeof(value), uintptr(16); got != want {
		t.Fatalf("sizeof(lockStatValue) = %d, want %d", got, want)
	}
	checks := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{name: "pid_tgid", got: unsafe.Offsetof(key.PidTgid), want: 0},
		{name: "comm", got: unsafe.Offsetof(key.Comm), want: 8},
		{name: "lock", got: unsafe.Offsetof(key.Lock), want: 24},
		{name: "kernel stack", got: unsafe.Offsetof(key.Kernstack), want: 32},
		{name: "user stack", got: unsafe.Offsetof(key.Userstack), want: 36},
		{name: "lock type", got: unsafe.Offsetof(key.LockType), want: 40},
	}
	for _, check := range checks {
		if check.got != check.want {
			t.Errorf("offset(%s) = %d, want %d", check.name, check.got, check.want)
		}
	}
}

func TestLockAggregationAndModes(t *testing.T) {
	aggr := &nativeAggregator{lockAggrMap: make(map[string]*lockStackEntry)}
	record := func(wait, count uint64) *lockStackEntry {
		return &lockStackEntry{
			Proc:      &processIDNameLock{Pid: 42, Name: "worker", Lock: 0xabcd},
			User:      "userA;userB;",
			Kernel:    "kernelA;",
			WaitTime:  wait,
			Contended: count,
			LockType:  "mutex",
		}
	}
	aggr.Aggregate(record(100, 1))
	aggr.Aggregate(record(250, 2))
	if len(aggr.lockAggrMap) != 1 {
		t.Fatalf("lock aggregate size = %d, want 1", len(aggr.lockAggrMap))
	}
	for _, got := range aggr.lockAggrMap {
		if got.WaitTime != 350 || got.Contended != 3 {
			t.Fatalf("aggregate = wait %d count %d, want wait 350 count 3", got.WaitTime, got.Contended)
		}
		_, timeValue := lockPrefixFrames(got, "time")
		if timeValue != 350 {
			t.Errorf("time mode value = %d, want 350", timeValue)
		}
		_, countValue := lockPrefixFrames(got, "count")
		if countValue != 3 {
			t.Errorf("count mode value = %d, want 3", countValue)
		}
	}
}

func TestLockAggregationKeepsDistinctDimensions(t *testing.T) {
	aggr := &nativeAggregator{lockAggrMap: make(map[string]*lockStackEntry)}
	base := lockStackEntry{
		Proc:      &processIDNameLock{Pid: 42, Name: "worker", Lock: 0xabcd},
		User:      "user;",
		Kernel:    "kernel-a;",
		WaitTime:  1,
		Contended: 1,
		LockType:  "mutex",
	}
	aggr.Aggregate(&base)

	differentPID := base
	differentPID.Proc = &processIDNameLock{Pid: 43, Name: "worker", Lock: 0xabcd}
	aggr.Aggregate(&differentPID)
	differentKernel := base
	differentKernel.Kernel = "kernel-b;"
	aggr.Aggregate(&differentKernel)
	differentType := base
	differentType.LockType = "rwlock"
	aggr.Aggregate(&differentType)

	if got, want := len(aggr.lockAggrMap), 4; got != want {
		t.Fatalf("lock aggregate size = %d, want %d", got, want)
	}
}

func TestProfilerFilterConstantsByScope(t *testing.T) {
	tests := []struct {
		name string
		pctx *pcontext.ProfilerContext
		want map[string]any
	}{
		{
			name: "all",
			pctx: &pcontext.ProfilerContext{Scope: pcontext.ScopeAll},
			want: map[string]any{},
		},
		{
			name: "pid",
			pctx: &pcontext.ProfilerContext{Scope: pcontext.ScopePID, PIDs: []int{11}},
			want: map[string]any{
				"profiler_filter_pid":     uint32(11),
				"profiler_filter_threads": true,
			},
		},
		{
			name: "tgid",
			pctx: &pcontext.ProfilerContext{Scope: pcontext.ScopeTGID, PIDs: []int{12}},
			want: map[string]any{
				"profiler_filter_tgid": uint32(12),
			},
		},
		{
			name: "cgroup",
			pctx: &pcontext.ProfilerContext{Scope: pcontext.ScopeCgroup, CgroupID: 13},
			want: map[string]any{
				"profiler_filter_cgroup_id": uint64(13),
			},
		},
		{
			name: "process group",
			pctx: &pcontext.ProfilerContext{Scope: pcontext.ScopeProcessGroup, ProcessGroupID: 14},
			want: map[string]any{
				"profiler_filter_process_group": uint32(14),
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			constants, err := profilerFilterConstants(tc.pctx, 99)
			if err != nil {
				t.Fatalf("profilerFilterConstants() error = %v", err)
			}
			want := map[string]any{
				"profiler_filter_css":           uint64(99),
				"profiler_filter_pid":           uint32(0),
				"profiler_filter_threads":       false,
				"profiler_filter_tgid":          uint32(0),
				"profiler_filter_cgroup_id":     uint64(0),
				"profiler_filter_process_group": uint32(0),
			}
			for name, value := range tc.want {
				want[name] = value
			}
			if !reflect.DeepEqual(constants, want) {
				t.Fatalf("profilerFilterConstants() = %#v, want %#v", constants, want)
			}
		})
	}
}
