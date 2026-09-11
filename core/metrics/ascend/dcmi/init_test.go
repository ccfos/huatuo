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

package dcmi

import "testing"

type fakeDynamicLibrary struct {
	openCalls  int
	closeCalls int
}

func (f *fakeDynamicLibrary) Open() error {
	f.openCalls++
	return nil
}

func (f *fakeDynamicLibrary) Close() error {
	f.closeCalls++
	return nil
}

func (f *fakeDynamicLibrary) Handle() uintptr {
	return 1
}

func TestDcInitRollsBackFailedInitializationAndAllowsRetry(t *testing.T) {
	fake := installFakeLibrary(t)
	initCalls := 0
	dcmiInit = func() Return {
		initCalls++
		if initCalls == 1 {
			return Return(-8005)
		}
		return Success
	}

	if err := DcInit(); err == nil {
		t.Fatal("first DcInit() error = nil, want initialization failure")
	}
	if fake.openCalls != 1 || fake.closeCalls != 1 || libdcmi.refcount != 0 {
		t.Fatalf(
			"after failed init: open calls = %d, close calls = %d, refcount = %d; want 1, 1, 0",
			fake.openCalls, fake.closeCalls, libdcmi.refcount,
		)
	}

	if err := DcInit(); err != nil {
		t.Fatalf("retry DcInit() error = %v", err)
	}
	if fake.openCalls != 2 || fake.closeCalls != 1 || libdcmi.refcount != 1 {
		t.Fatalf(
			"after successful retry: open calls = %d, close calls = %d, refcount = %d; want 2, 1, 1",
			fake.openCalls, fake.closeCalls, libdcmi.refcount,
		)
	}

	if err := DcShutDown(); err != nil {
		t.Fatalf("DcShutDown() error = %v", err)
	}
	if fake.closeCalls != 2 || libdcmi.refcount != 0 {
		t.Fatalf(
			"after shutdown: close calls = %d, refcount = %d; want 2, 0",
			fake.closeCalls, libdcmi.refcount,
		)
	}
}

func TestDcInitSuccessfulReferenceLifecycle(t *testing.T) {
	fake := installFakeLibrary(t)
	dcmiInit = func() Return { return Success }

	if err := DcInit(); err != nil {
		t.Fatalf("first DcInit() error = %v", err)
	}
	if err := DcInit(); err != nil {
		t.Fatalf("second DcInit() error = %v", err)
	}
	if fake.openCalls != 1 || libdcmi.refcount != 2 {
		t.Fatalf("after init: open calls = %d, refcount = %d; want 1, 2", fake.openCalls, libdcmi.refcount)
	}

	if err := DcShutDown(); err != nil {
		t.Fatalf("first DcShutDown() error = %v", err)
	}
	if fake.closeCalls != 0 || libdcmi.refcount != 1 {
		t.Fatalf("after first shutdown: close calls = %d, refcount = %d; want 0, 1", fake.closeCalls, libdcmi.refcount)
	}

	if err := DcShutDown(); err != nil {
		t.Fatalf("second DcShutDown() error = %v", err)
	}
	if fake.closeCalls != 1 || libdcmi.refcount != 0 {
		t.Fatalf("after final shutdown: close calls = %d, refcount = %d; want 1, 0", fake.closeCalls, libdcmi.refcount)
	}
}

func installFakeLibrary(t *testing.T) *fakeDynamicLibrary {
	t.Helper()
	originalLibrary := libdcmi
	originalInit := dcmiInit
	t.Cleanup(func() {
		libdcmi = originalLibrary
		dcmiInit = originalInit
	})

	fake := &fakeDynamicLibrary{}
	libdcmi = &library{
		dl:              fake,
		registerSymbols: func(uintptr) {},
	}
	return fake
}
