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

import "testing"

// resetForTest wipes libmtml to a known empty state. Tests must call this
// in setup so a previous test's dlopen does not leak into the next.
// Symmetric with the resetLibrary helper in init_test.go but kept here so
// the smoke test file is self-contained.
//
// IMPORTANT: the caller must NOT hold any other reference to libmtml
// (e.g. be inside IsReady) when this runs. libmtml.Mutex is not reentrant.
func resetForTest() {
	libmtml.Lock()
	libmtml.handle = 0
	libmtml.loadedAs = ""
	libmtml.lib = 0
	libmtml.ready = false
	libmtml.Unlock()
}

// TestSmoke_InitCountVersion exercises the registry-free path: load the
// .so, count devices, fetch the library version. The test is skipped on
// hosts without libmtml.so (CI runners, dev boxes).
func TestSmoke_InitCountVersion(t *testing.T) {
	resetForTest()
	if err := Init(); err != nil {
		t.Skipf("libmtml.so not available on this host: %v", err)
	}
	t.Cleanup(func() {
		_ = Shutdown()
		resetForTest()
	})

	if !IsReady() {
		t.Fatal("IsReady() = false after successful Init")
	}

	count, err := CountDevice()
	if err != nil {
		t.Fatalf("CountDevice: %v", err)
	}
	t.Logf("detected %d device(s)", count)

	// LibraryVersion is optional (mtmlLibraryGetVersion may be absent
	// from older .so builds); a NotSupported return is acceptable.
	if v, err := LibraryVersion(); err != nil {
		if !IsNotSupported(err) {
			t.Fatalf("LibraryVersion: %v", err)
		}
	} else {
		t.Logf("library version: %q", v)
	}
}

// TestSmoke_ShutdownReinitCycle exercises the lifecycle that
// mthreadsGpuCollector.Update() takes on a recoverable error: the
// .so stays mapped (pymtml-style), only the MtmlLibrary* interface
// handle is re-issued. The second Init must succeed and CountDevice
// must return the same number of devices.
func TestSmoke_ShutdownReinitCycle(t *testing.T) {
	resetForTest()
	if err := Init(); err != nil {
		t.Skipf("libmtml.so not available on this host: %v", err)
	}

	first, err := CountDevice()
	if err != nil {
		t.Fatalf("first CountDevice: %v", err)
	}
	if first == 0 {
		_ = Shutdown()
		resetForTest()
		t.Skip("no devices attached; reinit cycle is meaningful only with devices")
	}

	if err := Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if IsReady() {
		t.Fatal("IsReady() = true after Shutdown, want false")
	}

	if err := Init(); err != nil {
		t.Fatalf("re-Init after Shutdown: %v", err)
	}
	if !IsReady() {
		t.Fatal("IsReady() = false after re-Init, want true")
	}
	t.Cleanup(func() {
		_ = Shutdown()
		resetForTest()
	})

	second, err := CountDevice()
	if err != nil {
		t.Fatalf("second CountDevice: %v", err)
	}
	if second != first {
		t.Fatalf("device count changed across reinit: first=%d second=%d", first, second)
	}
}

// TestSmoke_RepeatedShutdownIsNoop verifies that calling Shutdown
// multiple times after Init does not return an error or panic. The
// collector reinit path sets ready=false but then may call Shutdown
// again on the next recovery; this test pins the no-op contract.
func TestSmoke_RepeatedShutdownIsNoop(t *testing.T) {
	resetForTest()
	if err := Init(); err != nil {
		t.Skipf("libmtml.so not available on this host: %v", err)
	}

	for i := 0; i < 3; i++ {
		if err := Shutdown(); err != nil {
			t.Fatalf("Shutdown #%d: %v", i+1, err)
		}
	}
	// And one more Init/Shutdown after the repeated Shutdowns to
	// confirm the .so mapping is still usable.
	if err := Init(); err != nil {
		t.Fatalf("Init after repeated Shutdown: %v", err)
	}
	if err := Shutdown(); err != nil {
		t.Fatalf("final Shutdown: %v", err)
	}
	resetForTest()
}

// TestSmoke_NotReadyBeforeInit confirms the public gate: a fresh
// process has IsReady()==false and any device API returns
// errNotReady (recognized by IsNotReady). This is the contract that
// protects every public entry point from a nil/nil-handle crash.
func TestSmoke_NotReadyBeforeInit(t *testing.T) {
	resetForTest()
	t.Cleanup(resetForTest)

	if IsReady() {
		t.Fatal("IsReady() = true on a freshly reset library")
	}
	if _, err := CountDevice(); !IsNotReady(err) {
		t.Fatalf("CountDevice on not-ready library returned %v, want not-ready", err)
	}
	if _, err := InitDeviceByIndex(0); !IsNotReady(err) {
		t.Fatalf("InitDeviceByIndex on not-ready library returned %v, want not-ready", err)
	}
}
