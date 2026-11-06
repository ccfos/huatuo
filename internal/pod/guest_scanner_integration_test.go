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
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/procfs"
)

// Only a test-owned process enters these isolated cgroups. The restricted proc
// view uses real kernel stat/cgroup files without scanning unrelated workloads.
func TestGuestSourceKernelLifecycle(t *testing.T) {
	prefix := fmt.Sprintf("huatuo-guest-%d-%d", os.Getpid(), time.Now().UnixNano())
	membership := "/" + prefix + "/" + guestCheckoutID
	roots := []string{cgroups.RootFsFilePath("cpu"), cgroups.RootFsFilePath("memory")}
	if cgroups.CgroupMode() == cgroups.Unified {
		roots = []string{cgroups.RootfsDefaultPath()}
	}
	directories := make(map[string]struct{})
	for _, root := range roots {
		root, err := filepath.EvalSymlinks(root)
		if err != nil {
			t.Fatal(err)
		}
		directory := filepath.Join(root, membership)
		if _, exists := directories[directory]; exists {
			continue
		}
		parent := filepath.Dir(directory)
		if err := os.Mkdir(parent, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Remove(parent); err != nil {
				t.Errorf("remove test cgroup parent: %v", err)
			}
		})
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		directories[directory] = struct{}{}
		t.Cleanup(func() {
			if err := os.Remove(directory); err != nil {
				t.Errorf("remove test cgroup: %v", err)
			}
		})
	}
	child := exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		if err := child.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Errorf("stop test process: %v", err)
		}
		_ = child.Wait()
	}
	t.Cleanup(stop)
	pid := strconv.Itoa(child.Process.Pid)
	for directory := range directories {
		if err := os.WriteFile(filepath.Join(directory, "cgroup.procs"), []byte(pid), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	procRoot := t.TempDir()
	guestFile(t, procRoot, "self/cgroup", "0::/\n")
	if err := os.Symlink(procfs.Path("self", "mountinfo"), filepath.Join(procRoot, "self", "mountinfo")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(procRoot, pid), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"stat", "cgroup"} {
		if err := os.Symlink(procfs.Path(pid, name), filepath.Join(procRoot, pid, name)); err != nil {
			t.Fatal(err)
		}
	}
	source := &guestScanner{options: GuestScannerOptions{
		ProcRoot: procRoot, CgroupRoot: cgroups.RootfsDefaultPath(),
		PodName: "checkout", PodNamespace: "integration",
	}}
	store := newTestContainerStore()
	previous := containerView
	containerView = store
	t.Cleanup(func() { containerView = previous })
	manager, err := newSourceManager(store, source, ManagerOptions{ProcRoot: procRoot})
	if err != nil {
		t.Fatal(err)
	}
	commit := func() {
		snapshot, err, _ := manager.discover(context.Background(), nil, true)
		if err != nil {
			t.Fatal(err)
		}
		store.commit(snapshot, nil)
	}
	commit()
	record := store.records[guestCheckoutID]
	if record == nil || record.container == nil {
		t.Fatal("Guest source did not publish test process")
	}
	if record.ref.InitPID != child.Process.Pid || record.ref.MemoryCgroupPath != membership || record.directory == nil {
		t.Errorf("Guest kernel reference=%+v", record.ref)
	}
	for _, controller := range []string{"cpu", "memory"} {
		if got, available := record.pod.Cgroups.Path(controller); !available || got != membership {
			t.Errorf("%s path=%q available=%t, want %q", controller, got, available, membership)
		}
	}
	if err := ValidateContainerRef(record.ref); err != nil {
		t.Errorf("validate Guest kernel binding: %v", err)
	}
	record.pod.lifeResources["baseline"] = uint64(6400)
	commit()
	current := store.records[guestCheckoutID]
	if current.ref.Key != record.ref.Key || current.pod.LifeResources("baseline") != uint64(6400) ||
		!current.container.StartedAt.Equal(record.container.StartedAt) || !current.container.SyncedAt.Equal(record.container.SyncedAt) {
		t.Error("refresh changed Guest instance state or legacy timestamps")
	}
	stop()
	commit()
	if len(store.records) != 0 {
		t.Error("complete discovery retained the exited Guest process")
	}
	if err := ValidateContainerRef(record.ref); err == nil {
		t.Error("deleted Guest reference remains valid")
	}
}
