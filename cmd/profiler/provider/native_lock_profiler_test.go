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
	"testing"
	"unsafe"

	"huatuo-bamai/internal/bpf"
	pcontext "huatuo-bamai/internal/profiler/context"
)

func TestLockAttachOptionsSupportsAllKernelLockTypes(t *testing.T) {
	available := map[string]bool{
		"mutex_lock":      true,
		"_raw_spin_lock":  true,
		"_raw_read_lock":  true,
		"_raw_write_lock": true,
	}
	old := hasLockKprobeFunction
	hasLockKprobeFunction = func(name string) bool { return available[name] }
	defer func() { hasLockKprobeFunction = old }()

	got, err := lockAttachOptions([]string{"mutex", "spinlock", "rwlock"})
	if err != nil {
		t.Fatalf("lockAttachOptions() error = %v", err)
	}
	want := []bpf.AttachOption{
		{ProgramName: "trace_mutex_lock", Symbol: "mutex_lock"},
		{ProgramName: "trace_mutex_lock_return", Symbol: "mutex_lock"},
		{ProgramName: "trace_spin_lock", Symbol: "_raw_spin_lock"},
		{ProgramName: "trace_spin_lock_return", Symbol: "_raw_spin_lock"},
		{ProgramName: "trace_rw_lock", Symbol: "_raw_read_lock"},
		{ProgramName: "trace_rw_lock_return", Symbol: "_raw_read_lock"},
		{ProgramName: "trace_rw_lock", Symbol: "_raw_write_lock"},
		{ProgramName: "trace_rw_lock_return", Symbol: "_raw_write_lock"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lockAttachOptions() = %#v, want %#v", got, want)
	}
}

func TestLockAttachOptionsAttachesEveryAvailableVariant(t *testing.T) {
	available := map[string]bool{
		"mutex_lock":               true,
		"mutex_lock_interruptible": true,
		"_raw_spin_lock":           true,
		"_raw_spin_lock_irqsave":   true,
	}
	old := hasLockKprobeFunction
	hasLockKprobeFunction = func(name string) bool { return available[name] }
	defer func() { hasLockKprobeFunction = old }()

	got, err := lockAttachOptions([]string{"mutex", "spinlock", "mutex"})
	if err != nil {
		t.Fatalf("lockAttachOptions() error = %v", err)
	}
	want := []bpf.AttachOption{
		{ProgramName: "trace_mutex_lock", Symbol: "mutex_lock"},
		{ProgramName: "trace_mutex_lock_return", Symbol: "mutex_lock"},
		{ProgramName: "trace_mutex_lock", Symbol: "mutex_lock_interruptible"},
		{ProgramName: "trace_mutex_lock_return", Symbol: "mutex_lock_interruptible"},
		{ProgramName: "trace_spin_lock", Symbol: "_raw_spin_lock"},
		{ProgramName: "trace_spin_lock_return", Symbol: "_raw_spin_lock"},
		{ProgramName: "trace_spin_lock", Symbol: "_raw_spin_lock_irqsave"},
		{ProgramName: "trace_spin_lock_return", Symbol: "_raw_spin_lock_irqsave"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lockAttachOptions() = %#v, want %#v", got, want)
	}
}

func TestLockAttachOptionsReportsUnavailableType(t *testing.T) {
	old := hasLockKprobeFunction
	hasLockKprobeFunction = func(string) bool { return false }
	defer func() { hasLockKprobeFunction = old }()

	if _, err := lockAttachOptions([]string{"mutex"}); err == nil {
		t.Fatal("lockAttachOptions() error = nil")
	}
}

func TestLockAttachOptionsRequiresReadAndWriteRWLock(t *testing.T) {
	old := hasLockKprobeFunction
	hasLockKprobeFunction = func(name string) bool { return name == "_raw_read_lock" }
	defer func() { hasLockKprobeFunction = old }()

	if _, err := lockAttachOptions([]string{"rwlock"}); err == nil {
		t.Fatal("lockAttachOptions() error = nil when write-side rwlock probe is unavailable")
	}
}

func TestLockEventBinaryLayout(t *testing.T) {
	var event lockEvent
	if got, want := unsafe.Sizeof(event), uintptr(64); got != want {
		t.Fatalf("sizeof(lockEvent) = %d, want %d", got, want)
	}
	checks := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{name: "base", got: unsafe.Offsetof(event.ProfilerEventBase), want: 0},
		{name: "lock", got: unsafe.Offsetof(event.Lock), want: 40},
		{name: "wait time", got: unsafe.Offsetof(event.WaitTime), want: 48},
		{name: "contended", got: unsafe.Offsetof(event.Contended), want: 56},
		{name: "lock type", got: unsafe.Offsetof(event.LockType), want: 60},
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
