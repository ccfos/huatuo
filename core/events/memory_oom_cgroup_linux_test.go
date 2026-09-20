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

package events

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/stats"
	"github.com/ccfos/huatuo/internal/pod"
)

func writeMemoryEventsForTest(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryCgroupRecovery(t *testing.T) {
	root := t.TempDir()
	w, err := openPressureWatcher(nil, &BeforeOOMConfig{}, cgroups.Unified, root)
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	events := make(chan memoryPressureEvent, 8)
	path := "/" + strings.Repeat("a", 64)
	w.containerPath = func(string) (string, error) { return "", os.ErrNotExist }
	if err := w.handleCgroupChange(t.Context(), events, pod.MemoryCgroupChange{ContainerID: filepath.Base(path)}); err != nil {
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
	writeMemoryEventsForTest(t, filepath.Join(dir, "memory.events"), "high 0\n")
	if err := w.recoverWatches(t.Context(), events); err != nil {
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
	writeMemoryEventsForTest(t, filepath.Join(dir, "memory.events"), "high 0\n")
	w.requestRecovery()
	if err := w.recoverWatches(t.Context(), events); err != nil {
		t.Fatal(err)
	}
	if w.cgroups[path] == nil || w.cgroups[path] == old {
		t.Fatal("recovery retained a replaced inode")
	}
	if err := os.Remove(filepath.Join(dir, "memory.events")); err != nil {
		t.Fatal(err)
	}
	w.removeCgroup(path)
	w.requestRecovery()
	for i := 0; i < 3; i++ {
		if err := w.recoverWatches(t.Context(), events); err != nil {
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
	root := t.TempDir()
	w, err := openPressureWatcher(nil, &BeforeOOMConfig{}, cgroups.Unified, root)
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	events := make(chan memoryPressureEvent, 8)
	create := func(id string) string {
		p := "/" + strings.Repeat(id, 64)
		if err := os.Mkdir(filepath.Join(root, p), 0o700); err != nil {
			t.Fatal(err)
		}
		writeMemoryEventsForTest(t, filepath.Join(root, p, "memory.events"), "high 0\n")
		return p
	}
	first := create("a")
	if err := w.refreshFromCgroupTree(t.Context(), events); err != nil {
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
	if err := w.handleCgroupChange(t.Context(), events, change); err != nil {
		t.Fatal(err)
	}
	if w.cgroups[target] == nil || w.cgroups[other] != nil {
		t.Fatal("event did not operate on only its target")
	}
	old := w.cgroups[target]
	if err := w.handleCgroupChange(t.Context(), events, change); err != nil {
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
	if err := w.handleCgroupChange(t.Context(), events, change); err != nil {
		t.Fatal(err)
	}
	if w.cgroups[target] == nil || w.cgroups[target] == old {
		t.Fatal("stale delete lost replacement")
	}
	writeMemoryEventsForTest(t, filepath.Join(root, target, "memory.events"), "high 1\n")
	if err := w.handleInotify(t.Context(), events); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		if event.cgroupPath != target {
			t.Fatalf("unexpected event: %+v", event)
		}
	default:
		t.Fatal("replacement pressure watch did not fire")
	}
	if err := os.RemoveAll(filepath.Join(root, target)); err != nil {
		t.Fatal(err)
	}
	if err := w.handleCgroupChange(t.Context(), events, change); err != nil {
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
	root := t.TempDir()
	w, err := openPressureWatcher(nil, &BeforeOOMConfig{}, cgroups.Unified, root)
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	id := strings.Repeat("a", 64)
	oldPath, newPath := "/old/"+id, "/new/"+id
	for _, path := range []string{oldPath, newPath} {
		dir := filepath.Join(root, path)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		writeMemoryEventsForTest(t, filepath.Join(dir, "memory.events"), "high 0\n")
	}
	if err := w.addCgroup(id, oldPath); err != nil {
		t.Fatal(err)
	}
	events := make(chan memoryPressureEvent, 1)
	w.containerPath = func(string) (string, error) { return "", os.ErrNotExist }
	change := pod.MemoryCgroupChange{ContainerID: id}
	if err := w.handleCgroupChange(t.Context(), events, change); err != nil {
		t.Fatal(err)
	}
	if w.cgroups[oldPath] == nil || w.recoveryDue.IsZero() {
		t.Fatal("lookup failure lost old watch or recovery")
	}
	w.containerPath = func(string) (string, error) { return newPath, nil }
	if err := w.handleCgroupChange(t.Context(), events, change); err != nil {
		t.Fatal(err)
	}
	if w.cgroups[oldPath] != nil || w.cgroups[newPath] == nil {
		t.Fatal("watch did not follow current path")
	}
	writeMemoryEventsForTest(t, filepath.Join(root, newPath, "memory.events"), "high 1\n")
	if err := w.handleInotify(t.Context(), events); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-events:
		if got.cgroupPath != newPath {
			t.Fatal("pressure attributed to old path")
		}
	default:
		t.Fatal("new path is not monitored")
	}
}

type lifecycleMemoryCgroup struct{ cgroups.Cgroup }

func (*lifecycleMemoryCgroup) MemoryUsage(string) (*stats.MemoryUsage, error) {
	return &stats.MemoryUsage{MaxLimited: 1 << 20}, nil
}
