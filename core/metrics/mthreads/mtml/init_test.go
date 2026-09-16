// Copyright 2026 The HuaTuo Authors
// Copyright 2026 The Mthreads Authors
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

package mtml

import (
	"errors"
	"fmt"
	"testing"
)

// resetLibrary wipes libmtml to a known-empty state. Tests must call this
// in setup so a previous test's dlopen does not leak into the next.
func resetLibrary(t *testing.T) {
	t.Helper()
	libmtml.Lock()
	defer libmtml.Unlock()
	libmtml.handle = 0
	libmtml.loadedAs = ""
	libmtml.lib = 0
	libmtml.ready = false
}

func TestIsRecoverable_TableDriven(t *testing.T) {
	cases := []struct {
		name string
		code Return
		want bool
	}{
		{"driver not loaded is recoverable", ErrorDriverNotLoaded, true},
		{"driver failure is recoverable", ErrorDriverFailure, true},
		{"resource busy is recoverable", ErrorResourceIsBusy, true},
		{"timeout is recoverable", ErrorTimeout, true},
		{"success is not", Success, false},
		{"invalid argument is not", ErrorInvalidArgument, false},
		{"not supported is not", ErrorNotSupported, false},
		{"unknown is not", ErrorUnknown, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := &Error{symbol: "test", code: tc.code}
			if got := IsRecoverable(err); got != tc.want {
				t.Fatalf("IsRecoverable(%v) = %v, want %v", tc.code, got, tc.want)
			}
		})
	}
}

func TestIsRecoverable_NilAndUnrelated(t *testing.T) {
	if IsRecoverable(nil) {
		t.Fatal("IsRecoverable(nil) = true, want false")
	}
	if IsRecoverable(errors.New("plain error")) {
		t.Fatal("IsRecoverable(plain) = true, want false")
	}
}

func TestIsRecoverable_JoinsAcrossLeaves(t *testing.T) {
	// One recoverable leaf among many non-recoverable leaves must still
	// trigger recovery. This mirrors the errors.Join tree built by
	// mthreadsGpuCollector.collect() (per-device workers each append
	// their own errs slice).
	joined := errors.Join(
		fmt.Errorf("gpu 0: name: %w", &Error{symbol: "mtmlDeviceGetName", code: ErrorInvalidArgument}),
		fmt.Errorf("gpu 0: pci sbdf: %w", &Error{symbol: "mtmlDeviceGetPciInfo", code: ErrorDriverFailure}),
		fmt.Errorf("gpu 0: bios: %w", &Error{symbol: "mtmlDeviceGetMtBiosVersion", code: ErrorNotFound}),
	)
	if !IsRecoverable(joined) {
		t.Fatal("IsRecoverable(joined with one recoverable leaf) = false, want true")
	}
}

func TestIsRecoverable_NestedErrorsJoin(t *testing.T) {
	// Two layers of errors.Join: a wrapped recoverable error inside a
	// group joined into a higher-level error.
	inner := errors.Join(
		fmt.Errorf("device sub: %w", &Error{symbol: "mtmlDeviceInitGpu", code: ErrorResourceIsBusy}),
	)
	outer := errors.Join(inner, fmt.Errorf("top-level: transient"))
	if !IsRecoverable(outer) {
		t.Fatal("nested errors.Join with recoverable leaf = false, want true")
	}
}

func TestEnforceInitFreePair_ConstructorWithoutDestructor(t *testing.T) {
	// If a constructor resolved but its matching destructor did not, the
	// constructor must be cleared so callers see a uniform NotSupported
	// rather than a SIGSEGV from a freed sub-handle.
	called := false
	ctor := func(uintptr, *uintptr) Return { called = true; return Success }
	enforceInitFreePair(&ctor, nil)
	if ctor != nil {
		t.Fatal("enforceInitFreePair did not clear the constructor when destructor is nil")
	}
	// And calling the now-nil function via a wrapper must not invoke the
	// underlying symbol (we only check it didn't get re-bound to something
	// dangerous — the variable is nil, so any direct call would NPE, which
	// the production code already guards against via nil checks).
	_ = called
}

func TestEnforceInitFreePair_BothPresentNoOp(t *testing.T) {
	ctor := func(uintptr, *uintptr) Return { return Success }
	free := func(uintptr) Return { return Success }
	enforceInitFreePair(&ctor, free)
	if ctor == nil {
		t.Fatal("enforceInitFreePair cleared the constructor when destructor was present")
	}
}

func TestEnforceInitFreePair_BothNilNoOp(t *testing.T) {
	// Both sides nil: nothing to enforce, must not panic. The init pointer
	// is a *func — it must be non-nil for the dereference to be safe;
	// the constructor variable itself may be nil (the value it points
	// to), which is the "both sides nil" case at the value level.
	var ctor func(uintptr, *uintptr) Return // nil
	enforceInitFreePair(&ctor, nil)
	if ctor != nil {
		t.Fatal("enforceInitFreePair modified a nil constructor")
	}
}

func TestShutdown_NotLoadedIsNoOp(t *testing.T) {
	resetLibrary(t)
	if err := Shutdown(); err != nil {
		t.Fatalf("Shutdown() on empty library returned %v, want nil", err)
	}
	if IsReady() {
		t.Fatal("IsReady() = true after Shutdown on empty library, want false")
	}
}

func TestShutdown_NoLibIsNoOp(t *testing.T) {
	// Simulate the post-Init-failure state: .so loaded (so Shutdown knows
	// the library exists) but lib == 0 (no interface handle to release).
	resetLibrary(t)
	libmtml.handle = 0xDEADBEEF // any non-zero sentinel
	libmtml.loadedAs = "libmtml.so.2"
	libmtml.lib = 0
	libmtml.ready = false

	if err := Shutdown(); err != nil {
		t.Fatalf("Shutdown() with lib=0 returned %v, want nil", err)
	}
}

func TestInit_WithoutLoadedSo_FailsAndLeavesReadyFalse(t *testing.T) {
	// No .so on this test box (CI), or loadLocked has no candidates:
	// Init must surface the error and leave ready=false. We do not assert
	// on the error text — the candidate list depends on the host.
	resetLibrary(t)
	err := Init()
	if err == nil {
		t.Skip("a real libmtml.so is mapped on this host; skipping synthetic fail test")
	}
	if IsReady() {
		t.Fatal("IsReady() = true after failed Init, want false")
	}
	if libmtml.lib != 0 {
		t.Fatal("libmtml.lib is non-zero after failed Init")
	}
}

func TestInit_ReadyShortCircuits(t *testing.T) {
	// If ready is already true (set by a prior successful Init), a second
	// Init must return nil without re-running loadLocked or mtmlLibraryInit.
	// We can't observe the latter directly, but we CAN observe that no
	// error is returned even though no .so is loaded — proving the early
	// return fired.
	//
	// The cleanup is critical: the test directly mutates the global
	// libmtml.ready without going through the real Init path, so without
	// reset the rest of the suite would see ready=true with nil function
	// pointers and panic on the first device call.
	resetLibrary(t)
	libmtml.ready = true
	t.Cleanup(func() { resetLibrary(t) })
	if err := Init(); err != nil {
		t.Fatalf("Init() on ready=true returned %v, want nil", err)
	}
}
