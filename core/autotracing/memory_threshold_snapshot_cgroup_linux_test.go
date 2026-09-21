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

package autotracing

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/memorywatch"
	"github.com/ccfos/huatuo/internal/cgroups/paths"
	"github.com/ccfos/huatuo/internal/pod"
)

func writeMemoryEventsForTest(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newTestPressureWatcher(t *testing.T) *pressureWatcher {
	t.Helper()
	previous := paths.RootfsDefaultPath
	paths.RootfsDefaultPath = t.TempDir()
	t.Cleanup(func() { paths.RootfsDefaultPath = previous })
	if err := os.MkdirAll(filepath.Join(paths.RootfsDefaultPath, "memory"), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := cgroups.MemoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	native, err := memorywatch.New(memorywatch.Options{ThresholdPercent: 90, MaxCgroups: maxWatchedCgroups})
	if err != nil {
		t.Fatal(err)
	}
	w := &pressureWatcher{
		watcher: native, root: root,
		cgroups: make(map[string]*watchedCgroup), targets: make(map[memorywatch.TargetID]*watchedCgroup),
		containers: make(map[string]string), pending: make(map[string]memoryPressureEvent),
		wake: make(chan struct{}, 1),
	}
	t.Cleanup(w.close)
	return w
}

func createMemoryCgroupForTest(t *testing.T, root, path string, usage uint64) {
	t.Helper()
	directory := filepath.Join(root, path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"memory.current": fmt.Sprint(usage), "memory.max": "100", "memory.high": "max",
		"memory.usage_in_bytes": fmt.Sprint(usage), "memory.limit_in_bytes": "100",
		"cgroup.event_control": "", "memory.events": "high 0\nmax 0\n",
	} {
		writeMemoryEventsForTest(t, filepath.Join(directory, name), value)
	}
}

func waitMemoryPressureForTest(t *testing.T, w *pressureWatcher, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var events [1]memorywatch.Event
	for {
		_, err := w.watcher.ReadEvents(ctx, events[:])
		if err != nil {
			t.Fatal(err)
		}
		if events[0].TargetID != w.cgroups[path].targetID || events[0].Kind != memorywatch.ThresholdObserved {
			continue
		}
		if err := w.handleMemoryEvent(ctx, &events[0]); err != nil {
			t.Fatal(err)
		}
		if w.pending[path].cgroupPath != path {
			t.Fatal("pressure missing from pending targets")
		}
		return
	}
}

func TestMemoryCgroupRecovery(t *testing.T) {
	w := newTestPressureWatcher(t)
	root := w.root
	path := "/" + strings.Repeat("a", 64)
	w.containerPath = func(string) (string, error) { return "", os.ErrNotExist }
	if err := w.handleCgroupChange(t.Context(), pod.MemoryCgroupChange{ContainerID: filepath.Base(path)}); err != nil {
		t.Fatal(err)
	}
	due := w.recoveryDue
	w.requestRecovery()
	if due.IsZero() || w.recoveryDue != due {
		t.Fatal("missing or uncoalesced recovery")
	}
	dir := filepath.Join(root, path)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	createMemoryCgroupForTest(t, root, path, 95)
	if err := w.recoverWatches(t.Context()); err != nil {
		t.Fatal(err)
	}
	old := w.cgroups[path]
	if old == nil || !w.recoveryDue.IsZero() {
		t.Fatal("recovery did not register the missed container")
	}
	if err := os.Rename(dir, filepath.Join(root, "retired")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	createMemoryCgroupForTest(t, root, path, 95)
	w.requestRecovery()
	if err := w.recoverWatches(t.Context()); err != nil {
		t.Fatal(err)
	}
	if w.cgroups[path] == nil || w.cgroups[path] == old {
		t.Fatal("recovery retained a replaced inode")
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := w.removeCgroup(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	w.requestRecovery()
	for i := 0; i < 3; i++ {
		if err := w.recoverWatches(t.Context()); err != nil {
			t.Fatal(err)
		}
		if i < 2 && w.recoveryDue.IsZero() {
			t.Fatal("registration failure did not retry")
		}
	}
	if !w.recoveryDue.IsZero() {
		t.Fatal("recovery exceeded its attempt budget")
	}
}

// Lifecycle handling must touch only the notified path, including when a
// delete from an old incarnation arrives after a new one was created.
func TestMemoryCgroupLifecycleTargetsCurrentPath(t *testing.T) {
	w := newTestPressureWatcher(t)
	root := w.root
	create := func(id string) string {
		p := "/" + strings.Repeat(id, 64)
		if err := os.Mkdir(filepath.Join(root, p), 0o700); err != nil {
			t.Fatal(err)
		}
		createMemoryCgroupForTest(t, root, p, 95)
		return p
	}
	first := create("a")
	if err := w.refreshFromCgroupTree(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(w.cgroups) != 1 {
		t.Fatal("initial cgroup not discovered")
	}
	other := create("b")
	target := create("c")
	lookups := 0
	w.containerPath = func(id string) (string, error) {
		lookups++
		return "/" + id, nil
	}
	change := pod.MemoryCgroupChange{ContainerID: filepath.Base(target)}
	if err := w.handleCgroupChange(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	if w.cgroups[target] == nil || w.cgroups[other] != nil {
		t.Fatal("event did not operate on only its target")
	}
	old := w.cgroups[target]
	if err := w.handleCgroupChange(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	if w.cgroups[target] != old {
		t.Fatal("duplicate create replaced live watch")
	}
	// Keep the old inode allocated so the filesystem cannot immediately reuse it.
	if err := os.Rename(filepath.Join(root, target), filepath.Join(root, "retired")); err != nil {
		t.Fatal(err)
	}
	create("c")
	change.Removed = true
	if err := w.handleCgroupChange(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	if w.cgroups[target] == nil || w.cgroups[target] == old {
		t.Fatal("stale delete lost replacement")
	}
	waitMemoryPressureForTest(t, w, target)
	if err := os.RemoveAll(filepath.Join(root, target)); err != nil {
		t.Fatal(err)
	}
	if err := w.handleCgroupChange(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	if w.cgroups[target] != nil || w.cgroups[first] == nil || w.cgroups[other] != nil {
		t.Fatal("delete changed unrelated watches")
	}
	if lookups != 2 {
		t.Fatalf("updates must resolve paths, deletes must use the cache: lookups=%d", lookups)
	}
}

func TestMemoryCgroupMovedWithOldPathPresent(t *testing.T) {
	w := newTestPressureWatcher(t)
	root := w.root
	id := strings.Repeat("a", 64)
	oldPath, newPath := "/old/"+id, "/new/"+id
	for _, path := range []string{oldPath, newPath} {
		dir := filepath.Join(root, path)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		createMemoryCgroupForTest(t, root, path, 95)
	}
	if err := w.addCgroup(t.Context(), id, oldPath); err != nil {
		t.Fatal(err)
	}
	w.containerPath = func(string) (string, error) { return "", os.ErrNotExist }
	change := pod.MemoryCgroupChange{ContainerID: id}
	if err := w.handleCgroupChange(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	if w.cgroups[oldPath] == nil || w.recoveryDue.IsZero() {
		t.Fatal("lookup failure lost old watch or recovery")
	}
	w.containerPath = func(string) (string, error) { return newPath, nil }
	if err := w.handleCgroupChange(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	if w.cgroups[oldPath] != nil || w.cgroups[newPath] == nil {
		t.Fatal("watch did not follow current path")
	}
	waitMemoryPressureForTest(t, w, newPath)
}
