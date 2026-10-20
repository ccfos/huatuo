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

//go:build integration && !didi

package pod

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestValidateContainerRefKernelAndRuntimeBindings(t *testing.T) {
	useContainerdForTest(t)
	root := t.TempDir()
	initMu.Lock()
	previousRoot := containerdStateDir
	containerdStateDir = root
	initMu.Unlock()
	t.Cleanup(func() { initMu.Lock(); containerdStateDir = previousRoot; initMu.Unlock() })
	previousView := containerView
	containerView = newTestContainerStore()
	t.Cleanup(func() { containerView = previousView })

	id := strings.Repeat("a", 64)
	directory := filepath.Join(root, "io.containerd.runtime.v2.task", "k8s.io", id)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(directory, "init.pid")
	for _, change := range []string{"unchanged", "runtime pid", "start time", "directory", "generation", "deleted", "unavailable"} {
		t.Run(change, func(t *testing.T) {
			if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
				t.Fatal(err)
			}
			record, err := containerMemoryBinding(&Container{ID: id, InitPid: os.Getpid()})
			if err != nil {
				t.Fatal(err)
			}
			records := map[string]*containerRecord{id: record}
			containerView.commit(containerSnapshot{records: records, isComplete: true}, nil)
			ref := record.ref
			info, err := MemoryCgroupDirectory(ref)
			if err != nil || !os.SameFile(info, record.directory) {
				t.Fatalf("published directory lookup: %v", err)
			}
			switch change {
			case "runtime pid":
				if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid()+1)), 0o600); err != nil {
					t.Fatal(err)
				}
			case "start time", "directory", "generation":
				updated := *record
				if change == "directory" {
					updated.directory, err = os.Stat(t.TempDir())
					if err != nil {
						t.Fatal(err)
					}
				} else {
					updated.startTime++
				}
				containerView.commit(containerSnapshot{records: map[string]*containerRecord{id: &updated}, isComplete: true}, nil)
				if change != "generation" {
					// Use the new reference to exercise live binding validation.
					ref = updated.ref
				}
			case "deleted":
				containerView.commit(containerSnapshot{isComplete: true}, nil)
			case "unavailable":
				containerView.commit(containerSnapshot{records: records}, ErrContainersUnavailable)
			}
			err = ValidateContainerRef(ref)
			if (err == nil) != (change == "unchanged") {
				t.Fatalf("validate %s binding: %v", change, err)
			}
		})
	}
}

func TestKataHostDoesNotPublishApplicationMemoryTarget(t *testing.T) {
	for _, test := range []struct {
		name, runtime, sandbox string
		available              bool
	}{
		{name: "ordinary", runtime: "io.containerd.runc.v2", available: true},
		{name: "Kata Host", runtime: "io.containerd.kata-qemu.v2", sandbox: "orders-vm"},
		{name: "Firecracker Host", runtime: "io.containerd.kata-fc.v2", sandbox: "orders-vm"},
		{name: "Guest", available: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			container := &Container{
				ID:          strings.Repeat("a", 64),
				InitPid:     os.Getpid(),
				RuntimeType: test.runtime,
				SandboxID:   test.sandbox,
				CgroupPath:  "/preserved-vm-cpu-path",
			}
			record, err := containerMemoryBinding(container)
			if err != nil {
				t.Fatal(err)
			}
			if got := record.ref.MemoryCgroupPath != ""; got != test.available {
				t.Errorf("memory target available=%t, want %t", got, test.available)
			}
			if record.ref.InitPID != container.InitPid ||
				container.CgroupPath != "/preserved-vm-cpu-path" ||
				record.directory == nil {
				t.Error("memory exclusion changed process or CPU identity")
			}
		})
	}
}
