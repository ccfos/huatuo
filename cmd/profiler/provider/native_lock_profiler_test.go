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
	t.Cleanup(func() { hasLockContentionTracepoints = oldTracepoints })
	hasLockContentionTracepoints = func() bool { return true }
	_, options, backend, err := lockAttachOptions(profiling.LockTypeMutex)
	if err != nil || backend != lockBackendContentionTracepoints || len(options) != 2 {
		t.Fatalf("mutex tracepoint options: backend=%q len=%d err=%v", backend, len(options), err)
	}
	hasLockContentionTracepoints = func() bool { return false }
	_, options, backend, err = lockAttachOptions(profiling.LockTypeMutex)
	if err != nil || backend != lockBackendMutexSlowpath || len(options) != 2 || options[1].RetprobeMaxActive != lockRetprobeMaxActive {
		t.Fatalf("mutex fallback options: backend=%q len=%d err=%v", backend, len(options), err)
	}
	if _, _, _, err := lockAttachOptions(profiling.LockTypeRWLock); err == nil {
		t.Fatal("rwlock should remain unsupported in mutex slice")
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
