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

package netutil

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestNetNamespaceInumByPID(t *testing.T) {
	tests := []struct {
		name    string
		pid     int
		wantErr bool
	}{
		{
			name:    "valid current pid",
			pid:     os.Getpid(),
			wantErr: false,
		},
		{
			name:    "invalid pid 0",
			pid:     0,
			wantErr: true,
		},
		{
			name:    "invalid negative pid",
			pid:     -1,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NetNamespaceInumByPID(tt.pid)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NetNamespaceInumByPID() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got == 0 {
				t.Errorf("NetNamespaceInumByPID() got = %v, want non-zero inum", got)
			}
		})
	}
}

// fakeNetNamespaceFile stands in for an opened /proc/.../ns/net entry.
type fakeNetNamespaceFile struct {
	fd     uintptr
	closes int
	err    error
}

func (f *fakeNetNamespaceFile) Fd() uintptr { return f.fd }

func (f *fakeNetNamespaceFile) Close() error {
	f.closes++
	return f.err
}

// namespaceSwitchRecorder records the syscall and scheduler edges so tests can
// assert both the namespace transitions and the thread-unlock decision.
type namespaceSwitchRecorder struct {
	setns   []int
	locks   int
	unlocks int
}

func (r *namespaceSwitchRecorder) ops(setnsErr func(int, int) error, read func() (uint64, error)) netNamespaceCookieOps {
	return netNamespaceCookieOps{
		setns: func(fd, nstype int) error {
			r.setns = append(r.setns, fd)
			if setnsErr != nil {
				return setnsErr(fd, nstype)
			}
			return nil
		},
		lock:   func() { r.locks++ },
		unlock: func() { r.unlocks++ },
		read:   read,
	}
}

func TestNetNamespaceCookieUnlocksAfterSuccessfulRestore(t *testing.T) {
	rec := &namespaceSwitchRecorder{}
	current := &fakeNetNamespaceFile{fd: 202}
	ops := rec.ops(nil, func() (uint64, error) { return 42, nil })

	got, err := ops.run(101, func() (netNamespaceFile, error) { return current, nil })
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if got != 42 {
		t.Fatalf("run() = %d, want 42", got)
	}
	// Target namespace is entered, then the caller's namespace is restored.
	if len(rec.setns) != 2 || rec.setns[0] != 101 || rec.setns[1] != 202 {
		t.Fatalf("setns calls = %v, want [101 202]", rec.setns)
	}
	if rec.locks != 1 || rec.unlocks != 1 {
		t.Fatalf("lock/unlock = %d/%d, want 1/1", rec.locks, rec.unlocks)
	}
	if current.closes != 1 {
		t.Fatalf("current namespace closes = %d, want 1", current.closes)
	}
}

func TestNetNamespaceCookieStaysLockedWhenRestoreFails(t *testing.T) {
	rec := &namespaceSwitchRecorder{}
	current := &fakeNetNamespaceFile{fd: 202}
	// The restore (second setns) fails; entering the target (first) succeeds.
	ops := rec.ops(func(int, int) error { return nil }, func() (uint64, error) { return 42, nil })
	ops.setns = func(fd, _ int) error {
		rec.setns = append(rec.setns, fd)
		if fd == 202 {
			return unix.EPERM
		}
		return nil
	}

	if _, err := ops.run(101, func() (netNamespaceFile, error) { return current, nil }); !errors.Is(err, unix.EPERM) {
		t.Fatalf("run() error = %v, want EPERM", err)
	}
	// The thread still carries the target namespace: it must not be unlocked.
	if rec.unlocks != 0 {
		t.Fatalf("unlocks = %d, want 0 (an unrestorable thread must stay locked)", rec.unlocks)
	}
	if rec.locks != 1 {
		t.Fatalf("locks = %d, want 1", rec.locks)
	}
}

func TestNetNamespaceCookieUnlocksWhenTargetEntryFails(t *testing.T) {
	rec := &namespaceSwitchRecorder{}
	current := &fakeNetNamespaceFile{fd: 202}
	ops := rec.ops(func(int, int) error { return unix.EINVAL }, func() (uint64, error) { return 42, nil })

	if _, err := ops.run(101, func() (netNamespaceFile, error) { return current, nil }); !errors.Is(err, unix.EINVAL) {
		t.Fatalf("run() error = %v, want EINVAL", err)
	}
	// The thread never left the original namespace, so it is safe to unlock.
	if rec.unlocks != 1 {
		t.Fatalf("unlocks = %d, want 1", rec.unlocks)
	}
	if len(rec.setns) != 1 {
		t.Fatalf("setns calls = %v, want one attempt", rec.setns)
	}
}

func TestNetNamespaceCookieRestoresAfterReadFailure(t *testing.T) {
	rec := &namespaceSwitchRecorder{}
	current := &fakeNetNamespaceFile{fd: 202}
	ops := rec.ops(nil, func() (uint64, error) { return 0, unix.ENOPROTOOPT })

	if _, err := ops.run(101, func() (netNamespaceFile, error) { return current, nil }); !errors.Is(err, unix.ENOPROTOOPT) {
		t.Fatalf("run() error = %v, want ENOPROTOOPT", err)
	}
	if len(rec.setns) != 2 {
		t.Fatalf("setns calls = %v, want a restore attempt", rec.setns)
	}
	if rec.unlocks != 1 {
		t.Fatalf("unlocks = %d, want 1", rec.unlocks)
	}
}

func TestNetNamespaceCookieUnlocksWhenCurrentOpenFails(t *testing.T) {
	rec := &namespaceSwitchRecorder{}
	ops := rec.ops(nil, func() (uint64, error) { return 42, nil })

	if _, err := ops.run(101, func() (netNamespaceFile, error) { return nil, unix.ENOENT }); !errors.Is(err, unix.ENOENT) {
		t.Fatalf("run() error = %v, want ENOENT", err)
	}
	if rec.unlocks != 1 {
		t.Fatalf("unlocks = %d, want 1", rec.unlocks)
	}
	if len(rec.setns) != 0 {
		t.Fatalf("setns calls = %v, want none", rec.setns)
	}
}
