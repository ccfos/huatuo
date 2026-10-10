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

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/ccfos/huatuo/cmd/huatuo-bamai/config"
	"github.com/ccfos/huatuo/internal/cgroups"

	"github.com/opencontainers/runtime-spec/specs-go"
)

func TestSetupCgroupDisabledByDefault(t *testing.T) {
	cleanup, err := setupCgroup(&Daemon{opts: &Options{}})
	if err != nil {
		t.Fatalf("setupCgroup() error = %v", err)
	}
	if cleanup != nil {
		t.Fatal("setupCgroup() returned a cleanup function while disabled")
	}
}

// fakeCgroup replaces only the calls setupCgroup makes, through the embedded
// interface.
type fakeCgroup struct {
	cgroups.Cgroup

	newRuntimeErr error
	addProcErr    error
	deleteCalls   int
}

func (f *fakeCgroup) NewRuntime(string, *specs.LinuxResources) error { return f.newRuntimeErr }

func (f *fakeCgroup) AddProc(uint64) error { return f.addProcErr }

func (f *fakeCgroup) DeleteRuntime() error {
	f.deleteCalls++
	return nil
}

func useFakeCgroupManager(t *testing.T, fake *fakeCgroup) {
	t.Helper()
	previous := newCgroupManager
	newCgroupManager = func() (cgroups.Cgroup, error) { return fake, nil }
	t.Cleanup(func() { newCgroupManager = previous })
}

func loadConfigForTest(t *testing.T) {
	t.Helper()
	if err := config.Load("../../huatuo-bamai.conf"); err != nil {
		t.Fatalf("load config: %v", err)
	}
}

// TestSetupCgroupDeletesRuntimeWhenAddProcFails covers the leak: the runtime
// cgroup is created before the process is added to it, and the cleanup that
// removes it is only returned on success, so the failure path has to delete it.
func TestSetupCgroupDeletesRuntimeWhenAddProcFails(t *testing.T) {
	loadConfigForTest(t)
	fake := &fakeCgroup{addProcErr: errors.New("write cgroup.procs: permission denied")}
	useFakeCgroupManager(t, fake)

	cleanup, err := setupCgroup(&Daemon{opts: &Options{EnableCgroup: true}})
	if err == nil {
		t.Fatal("setupCgroup() error = nil, want the AddProc error")
	}
	if cleanup != nil {
		t.Fatal("setupCgroup() returned a cleanup function for a failed setup")
	}
	if fake.deleteCalls != 1 {
		t.Fatalf("DeleteRuntime() calls = %d, want 1: the runtime cgroup created before the failure outlives the process otherwise", fake.deleteCalls)
	}
}

func TestSetupCgroupKeepsRuntimeUntilCleanup(t *testing.T) {
	loadConfigForTest(t)
	fake := &fakeCgroup{}
	useFakeCgroupManager(t, fake)

	daemon := &Daemon{opts: &Options{EnableCgroup: true}}
	cleanup, err := setupCgroup(daemon)
	if err != nil {
		t.Fatalf("setupCgroup() error = %v", err)
	}
	if cleanup == nil {
		t.Fatal("setupCgroup() cleanup = nil on success")
	}
	if daemon.cgr == nil {
		t.Fatal("setupCgroup() did not store the cgroup manager")
	}
	if fake.deleteCalls != 0 {
		t.Fatalf("DeleteRuntime() calls = %d before the cleanup, want 0", fake.deleteCalls)
	}

	if err := cleanup(context.Background()); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
	if fake.deleteCalls != 1 {
		t.Fatalf("DeleteRuntime() calls = %d after the cleanup, want 1", fake.deleteCalls)
	}
}
