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
	"encoding/binary"
	"errors"
	"testing"
	"time"

	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/pkg/profiling"
)

func TestValidateLockTarget(t *testing.T) {
	for _, pctx := range []*pcontext.ProfilerContext{
		{PIDs: []int{42}},
		{ContainerID: "container"},
	} {
		if err := validateLockTarget(pctx); err != nil {
			t.Errorf("validateLockTarget(%+v) error = %v", pctx, err)
		}
	}
	for _, pctx := range []*pcontext.ProfilerContext{
		{},
		{PIDs: []int{42}, ContainerID: "container"},
	} {
		if err := validateLockTarget(pctx); err == nil {
			t.Errorf("validateLockTarget(%+v) error = nil", pctx)
		}
	}
}

func TestLockAttachOptions(t *testing.T) {
	oldTracepoints := hasLockContentionTracepoints
	t.Cleanup(func() {
		hasLockContentionTracepoints = oldTracepoints
	})

	hasLockContentionTracepoints = func() bool { return true }
	for _, test := range []struct {
		lockType profiling.LockType
		prefix   string
	}{
		{lockType: profiling.LockTypeMutex, prefix: "mutex"},
		{lockType: profiling.LockTypeRWLock, prefix: "rwlock"},
	} {
		_, options, backend, err := lockAttachOptions(test.lockType)
		if err != nil {
			t.Fatalf("lockAttachOptions(%q) error = %v", test.lockType, err)
		}
		if backend != lockBackendContentionTracepoints || len(options) != 2 {
			t.Errorf(
				"lockAttachOptions(%q) = backend %q, options %d",
				test.lockType,
				backend,
				len(options),
			)
		}
		if options[0].ProgramName !=
			"trace_"+test.prefix+"_contention_begin" ||
			options[1].ProgramName !=
				"trace_"+test.prefix+"_contention_end" {
			t.Errorf(
				"lockAttachOptions(%q) programs = %q, %q",
				test.lockType,
				options[0].ProgramName,
				options[1].ProgramName,
			)
		}
	}

	hasLockContentionTracepoints = func() bool { return false }
	if _, options, backend, err := lockAttachOptions(
		profiling.LockTypeMutex,
	); err != nil || backend != lockBackendMutexSlowpath ||
		len(options) != 2 {
		t.Errorf(
			"mutex fallback = backend %q, options %d, error %v",
			backend,
			len(options),
			err,
		)
	} else if options[0].RetprobeMaxActive != 0 ||
		options[1].RetprobeMaxActive != lockRetprobeMaxActive {
		t.Errorf(
			"mutex fallback maxactive = %d, %d",
			options[0].RetprobeMaxActive,
			options[1].RetprobeMaxActive,
		)
	}
	if _, options, backend, err := lockAttachOptions(
		profiling.LockTypeRWLock,
	); err != nil || backend != lockBackendRWLockSlowpaths ||
		len(options) != 4 {
		t.Errorf(
			"rwlock fallback = backend %q, options %d, error %v",
			backend,
			len(options),
			err,
		)
	} else if options[0].RetprobeMaxActive != 0 ||
		options[1].RetprobeMaxActive != lockRetprobeMaxActive ||
		options[2].RetprobeMaxActive != 0 ||
		options[3].RetprobeMaxActive != lockRetprobeMaxActive {
		t.Errorf(
			"rwlock fallback maxactive = %d, %d, %d, %d",
			options[0].RetprobeMaxActive,
			options[1].RetprobeMaxActive,
			options[2].RetprobeMaxActive,
			options[3].RetprobeMaxActive,
		)
	}
	if _, _, _, err := lockAttachOptions(profiling.LockTypeSpinlock); err == nil {
		t.Fatal("spinlock should remain unsupported in rwlock slice")
	}
}

func TestLockStatLayoutAndAccess(t *testing.T) {
	if got := binary.Size(lockStatKey{}); got != 48 {
		t.Fatalf("lockStatKey binary size = %d, want 48", got)
	}
	if got := binary.Size(lockStatValue{}); got != 16 {
		t.Fatalf("lockStatValue binary size = %d, want 16", got)
	}
	if lockAccessName(1) != "read" ||
		lockAccessName(2) != "write" ||
		lockAccessName(0) != "" {
		t.Fatal("lock access names are inconsistent")
	}
}

func TestWaitForLockWriters(t *testing.T) {
	active := []uint64{2, 1, 0}
	reads := 0
	err := waitForLockWriters(func() (uint64, error) {
		value := active[reads]
		reads++
		return value, nil
	}, time.Second)
	if err != nil {
		t.Fatalf("waitForLockWriters() error = %v", err)
	}
	if reads != len(active) {
		t.Fatalf("active writer reads = %d, want %d", reads, len(active))
	}
}

func TestWaitForLockWritersReturnsReadError(t *testing.T) {
	want := errors.New("read failed")
	err := waitForLockWriters(func() (uint64, error) {
		return 0, want
	}, time.Second)
	if !errors.Is(err, want) {
		t.Fatalf("waitForLockWriters() error = %v, want %v", err, want)
	}
}

func TestSumPerCPULockCounter(t *testing.T) {
	value := make([]byte, 24)
	binary.LittleEndian.PutUint64(value[0:8], 2)
	binary.LittleEndian.PutUint64(value[8:16], 3)
	binary.LittleEndian.PutUint64(value[16:24], 5)

	got, err := sumPerCPULockCounter(value)
	if err != nil {
		t.Fatalf("sumPerCPULockCounter() error = %v", err)
	}
	if got != 10 {
		t.Fatalf("sumPerCPULockCounter() = %d, want 10", got)
	}
	if _, err := sumPerCPULockCounter(make([]byte, 7)); err == nil {
		t.Fatal("sumPerCPULockCounter() malformed error = nil")
	}
}
