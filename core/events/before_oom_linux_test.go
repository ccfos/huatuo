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
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/stats"
	"github.com/ccfos/huatuo/internal/memsnap"
	"github.com/ccfos/huatuo/internal/memsnap/collector"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/tracing"
)

// Block discovery so cancellation cannot race past the cleanup assertion.
type blockingMemoryCgroup struct {
	cgroups.Cgroup
	entered chan struct{}
	release chan struct{}
}

func (c *blockingMemoryCgroup) MemoryUsage(string) (*stats.MemoryUsage, error) {
	close(c.entered)
	<-c.release
	return &stats.MemoryUsage{MaxLimited: 1 << 20}, nil
}

func TestBeforeOOMStopJoinsWatcherBeforeRestart(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, strings.Repeat("a", 64))
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"memory.limit_in_bytes", "memory.usage_in_bytes", "cgroup.event_control"} {
		writeMemoryEventsForTest(t, filepath.Join(directory, name), "0")
	}
	cfg := &BeforeOOMConfig{ThresholdPercent: 90}
	snapshot := &beforeOOMMemsnap{}
	for iteration := 0; iteration < 2; iteration++ {
		t.Run(strconv.Itoa(iteration), func(t *testing.T) {
			backend := &blockingMemoryCgroup{entered: make(chan struct{}), release: make(chan struct{})}
			release := sync.OnceFunc(func() { close(backend.release) })
			watcher, err := openPressureWatcher(backend, cfg, cgroups.Legacy, root)
			if err != nil {
				t.Fatal(err)
			}
			// Ordinary files exercise v1 FD registration without changing host cgroups.
			watcher.root, watcher.mode = root, cgroups.Legacy
			fds := []int{watcher.epollFD, watcher.inotifyFD, watcher.controlFD}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() {
				done <- snapshot.watchAndCapture(ctx, cfg, watcher)
				close(done)
			}()
			t.Cleanup(func() {
				cancel()
				release()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("watcher did not stop")
				}
			})
			select {
			case <-backend.entered:
			case <-time.After(time.Second):
				t.Fatal("watcher did not start discovery")
			}
			cancel()
			select {
			case err := <-done:
				t.Fatalf("tracer returned before watcher cleanup: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			release()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("tracer did not stop after releasing discovery")
			}
			control, err := os.ReadFile(filepath.Join(directory, "cgroup.event_control"))
			if err != nil {
				t.Fatal(err)
			}
			var pressureFD int
			if _, err := fmt.Sscanf(string(control), "%d", &pressureFD); err != nil {
				t.Fatal(err)
			}
			for _, fd := range append(fds, pressureFD) {
				if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
					t.Errorf("FD %d remains open after stop: %v", fd, err)
				}
			}
		})
	}
}

func writeMemoryEventsForTest(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestBeforeOOMRevalidatesContainerBeforePersistence(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(strconv.FormatBool(changed), func(t *testing.T) {
			path, saved := "/original", false
			identity := memsnap.ProcessIdentity{TGID: 42, StartTimeTicks: 100}
			result := &collector.Result{
				Identity: identity, Language: memsnap.LanguageGo,
				CaptureTime:   time.Now().UTC(),
				Snapshot:      &memsnap.Snapshot{Status: memsnap.StatusComplete},
				ProcessMemory: &memsnap.ProcessMemory{Status: memsnap.StatusComplete},
			}
			ops := &beforeOOMOps{
				selectVictim: func(context.Context, string, uint64) (victimCandidate, error) {
					return victimCandidate{pid: 42, identity: identity}, nil
				},
				validate: func(_ context.Context, path string, actual memsnap.ProcessIdentity) error {
					if path != "/original" || actual != identity {
						t.Fatal("selected identity or cgroup was replaced")
					}
					return nil
				},
				containerPath: func(string) (string, error) { return path, nil },
				collect: func(ctx context.Context, pid int, options collector.Options) (*collector.Result, error) {
					if pid != 42 || *options.ExpectedIdentity != identity || options.TopK != 10 ||
						options.GoTimeout != 100*time.Millisecond ||
						options.JavaTimeout != 2*time.Second || options.PythonTimeout != 2*time.Second {
						t.Fatalf("collector options = %+v, pid = %d", options, pid)
					}
					if err := options.CheckTarget(ctx, identity); err != nil {
						return nil, err
					}
					if changed {
						path = "/replacement"
					}
					return result, options.Save(ctx, result)
				},
				save: func(req *tracing.WriteRequest) error {
					saved = true
					data := req.TracerData.(*beforeOOMData)
					if data.Snapshot != result.Snapshot || data.Language != result.Language ||
						data.ProcessMemory != result.ProcessMemory ||
						!req.ObservedTimestamp.Equal(result.CaptureTime) {
						t.Fatalf("saved result = %+v", data)
					}
					return nil
				},
			}
			err := (&beforeOOMMemsnap{captureOps: ops}).captureCandidate(t.Context(),
				&BeforeOOMConfig{
					TopK: 10, GoTimeoutMS: 100,
					JavaTimeoutMS: 2000, PythonTimeoutMS: 2000,
				},
				&memcgCandidate{cgroupPath: "/original"})
			if changed && (err == nil || saved) {
				t.Fatalf("changed container path persisted: error=%v saved=%v", err, saved)
			}
			if !changed && (err != nil || !saved) {
				t.Fatalf("unchanged container not persisted: error=%v saved=%v", err, saved)
			}
		})
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

func TestMemoryCgroupLifecycleWakesEpoll(t *testing.T) {
	root := t.TempDir()
	create := func(letter string) string {
		t.Helper()
		p := "/" + strings.Repeat(letter, 64)
		dir := filepath.Join(root, p)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"memory.limit_in_bytes", "memory.usage_in_bytes", "cgroup.event_control"} {
			writeMemoryEventsForTest(t, filepath.Join(dir, name), "0")
		}
		return p
	}
	first := create("a")
	w, err := openPressureWatcher(&lifecycleMemoryCgroup{}, &BeforeOOMConfig{ThresholdPercent: 90}, cgroups.Legacy, root)
	if err != nil {
		t.Fatal(err)
	}
	changes := make(chan pod.MemoryCgroupChange, 1)
	w.changes = changes
	w.containerPath = func(id string) (string, error) { return "/" + id, nil }
	ctx, cancel := context.WithCancel(t.Context())
	events, done := w.Run(ctx)
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("watcher did not stop")
		}
	})
	waitPressure := func(want string) {
		t.Helper()
		select {
		case got := <-events:
			if got.cgroupPath != want {
				t.Fatalf("pressure = %+v, want %s", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("watcher was not woken")
		}
	}
	waitPressure(first) // Initial enumeration has completed before adding B.
	second := create("b")
	changes <- pod.MemoryCgroupChange{ContainerID: filepath.Base(second)}
	signalEventFD(w.controlFD)
	waitPressure(second)
}
