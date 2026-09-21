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

package memorywatch

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/stats"
	"github.com/ccfos/huatuo/internal/utils/parseutil"
)

type watchFixture struct {
	root  string
	mode  cgroups.Mode
	reads atomic.Int64
}

func newWatchFixture(t *testing.T, mode cgroups.Mode, maxTargets int) (*Watcher, *watchFixture) {
	t.Helper()
	f := &watchFixture{root: t.TempDir(), mode: mode}
	w, err := openWatcher(Options{ThresholdPercent: 90, MaxCgroups: maxTargets},
		mode, f.root, f.readUsage)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w, f
}

func (f *watchFixture) readUsage(path string) (*stats.MemoryUsage, error) {
	f.reads.Add(1)
	usageFile, limitFile := "memory.current", "memory.max"
	if f.mode != cgroups.Unified {
		usageFile, limitFile = "memory.usage_in_bytes", "memory.limit_in_bytes"
	}
	usage, err := parseutil.ReadUint(filepath.Join(f.root, path, usageFile))
	if err != nil {
		return nil, err
	}
	limit, err := parseutil.ReadUint(filepath.Join(f.root, path, limitFile))
	return &stats.MemoryUsage{Usage: usage, MaxLimited: limit}, err
}

func (f *watchFixture) create(t *testing.T, path string, usage, limit uint64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(f.root, path), 0o700); err != nil {
		t.Fatal(err)
	}
	f.write(t, path, "memory.current", fmt.Sprint(usage))
	f.write(t, path, "memory.max", fmt.Sprint(limit))
	f.write(t, path, "memory.high", "max")
	f.write(t, path, "memory.events", "high 0\nmax 0\noom 0\n")
	f.write(t, path, "memory.usage_in_bytes", fmt.Sprint(usage))
	f.write(t, path, "memory.limit_in_bytes", fmt.Sprint(limit))
	f.write(t, path, "cgroup.event_control", "")
}

func (f *watchFixture) write(t *testing.T, path, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.root, path, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readWatchEvent(t *testing.T, w *Watcher, kind EventKind) Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var events [1]Event
	for {
		n, err := w.ReadEvents(ctx, events[:])
		if err != nil {
			t.Fatal(err)
		}
		if n == 1 && events[0].Kind == kind {
			return events[0]
		}
	}
}

func TestWatcherRegistration(t *testing.T) {
	for _, mode := range []cgroups.Mode{cgroups.Legacy, cgroups.Unified} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			w, f := newWatchFixture(t, mode, 2)
			f.create(t, "/a", 95, 100)
			id, err := w.Add(t.Context(), "/a")
			if err != nil || id == 0 {
				t.Fatalf("Add = %d, %v", id, err)
			}
			again, err := w.Add(t.Context(), "/a")
			if err != nil || again != id {
				t.Fatalf("duplicate Add = %d, %v", again, err)
			}
			event := readWatchEvent(t, w, ThresholdObserved)
			if event.TargetID != id || event.UsageBytes != 95 || event.LimitBytes != 100 {
				t.Fatalf("initial event = %+v", event)
			}
			for i := 0; i < 2; i++ {
				if err := w.Remove(t.Context(), id); err != nil {
					t.Fatal(err)
				}
			}
			next, err := w.Add(t.Context(), "/a")
			if err != nil || next == id {
				t.Fatalf("new registration = %d, %v", next, err)
			}
			if err := w.Remove(t.Context(), next); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			var events [1]Event
			if _, err := w.ReadEvents(ctx, events[:]); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled read = %v", err)
			}
		})
	}
}

func TestWatcherCancellationRollsBack(t *testing.T) {
	f := &watchFixture{root: t.TempDir(), mode: cgroups.Legacy}
	f.create(t, "/a", 95, 100)
	entered, release := make(chan struct{}), make(chan struct{})
	once := sync.Once{}
	w, err := openWatcher(Options{ThresholdPercent: 90, MaxCgroups: 1},
		cgroups.Legacy, f.root, func(path string) (*stats.MemoryUsage, error) {
			once.Do(func() { close(entered); <-release })
			return f.readUsage(path)
		})
	if err != nil {
		t.Fatal(err)
	}
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(func() { unblock(); _ = w.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := w.Add(ctx, "/a"); result <- err }()
	<-entered
	cancel()
	select {
	case err := <-result:
		t.Fatalf("Add returned before rollback: %v", err)
	default:
	}
	unblock()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Add = %v", err)
	}
	if _, err := w.Add(t.Context(), "/a"); err != nil {
		t.Fatalf("canceled registration retained target budget: %v", err)
	}
}

func TestWatcherCloseWaitsAndUnblocks(t *testing.T) {
	w, f := newWatchFixture(t, cgroups.Unified, 2)
	f.create(t, "/a", 80, 100)
	fds := []int{w.epollFD, w.inotifyFD, w.controlFD}
	readDone := make(chan error, 1)
	go func() {
		var events [1]Event
		_, err := w.ReadEvents(t.Context(), events[:])
		readDone <- err
	}()
	var workers sync.WaitGroup
	for i := 0; i < 16; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			id, err := w.Add(t.Context(), "/a")
			if err == nil {
				err = w.Remove(t.Context(), id)
			}
			if err != nil && !errors.Is(err, ErrClosed) {
				t.Error(err)
			}
			if err := w.Close(); err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	if err := <-readDone; !errors.Is(err, ErrClosed) {
		t.Fatalf("blocked read = %v", err)
	}
	for _, fd := range fds {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
			t.Fatalf("fd %d remains open: %v", fd, err)
		}
	}
}

func TestWatcherReplacementAndCapacity(t *testing.T) {
	w, f := newWatchFixture(t, cgroups.Unified, 2)
	f.create(t, "/a", 95, 100)
	old, err := w.Add(t.Context(), "/a")
	if err != nil {
		t.Fatal(err)
	}
	readWatchEvent(t, w, ThresholdObserved)
	if err := os.Rename(filepath.Join(f.root, "a"), filepath.Join(f.root, "retired")); err != nil {
		t.Fatal(err)
	}
	f.create(t, "/a", 96, 100)
	next, err := w.Add(t.Context(), "/a")
	if err != nil || old == next {
		t.Fatalf("replacement = %d, %v", next, err)
	}
	if event := readWatchEvent(t, w, ThresholdObserved); event.TargetID != next {
		t.Fatalf("replacement event = %+v", event)
	}
	f.create(t, "/b", 0, 100)
	f.create(t, "/c", 0, 100)
	if _, err := w.Add(t.Context(), "/b"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Add(t.Context(), "/c"); !errors.Is(err, ErrTargetLimit) {
		t.Fatalf("over capacity = %v", err)
	}
}

func TestWatcherIdleAndUnlimited(t *testing.T) {
	w, f := newWatchFixture(t, cgroups.Unified, 1)
	f.create(t, "/a", 95, math.MaxUint64)
	if _, err := w.Add(t.Context(), "/a"); err != nil {
		t.Fatal(err)
	}
	event := readWatchEvent(t, w, TargetUnavailable)
	if !errors.Is(event.Err, ErrLimitUnavailable) {
		t.Fatalf("unlimited event = %+v", event)
	}
	reads := f.reads.Load()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	var events [1]Event
	if _, err := w.ReadEvents(ctx, events[:]); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("idle read = %v", err)
	}
	if f.reads.Load() != reads {
		t.Fatal("idle watcher sampled memory")
	}
	f.write(t, "/a", "memory.max", "100")
	readWatchEvent(t, w, ThresholdObserved)
}

func TestWatcherManyTargetsShareResources(t *testing.T) {
	for _, mode := range []cgroups.Mode{cgroups.Legacy, cgroups.Unified} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			w, f := newWatchFixture(t, mode, 128)
			before, err := os.ReadDir("/proc/self/fd")
			if err != nil {
				t.Fatal(err)
			}
			goroutines := runtime.NumGoroutine()
			for i := 0; i < 128; i++ {
				path := fmt.Sprintf("/target-%d", i)
				f.create(t, path, 95, 100)
				if _, err := w.Add(t.Context(), path); err != nil {
					t.Fatal(err)
				}
			}
			after, err := os.ReadDir("/proc/self/fd")
			if err != nil {
				t.Fatal(err)
			}
			wantFDs := 0
			if mode == cgroups.Legacy {
				wantFDs = 128
			}
			if len(after)-len(before) != wantFDs {
				t.Fatalf("target FD growth = %d, want %d", len(after)-len(before), wantFDs)
			}
			if got := runtime.NumGoroutine(); got > goroutines+2 {
				t.Fatalf("goroutines grew from %d to %d", goroutines, got)
			}
			// Registration still completes while every target has an unread event.
			var events [128]Event
			n, err := w.ReadEvents(t.Context(), events[:])
			if err != nil || n != len(events) {
				t.Fatalf("pending targets = %d, %v", n, err)
			}
		})
	}
}

func TestWatcherPendingCoalescesAndReportsOverflow(t *testing.T) {
	w, _ := newWatchFixture(t, cgroups.Unified, 2)
	// Publishing models native observations; the API must remain usable without a reader.
	_, err := w.submit(t.Context(), func() (TargetID, error) {
		for id := TargetID(1); id <= 2; id++ {
			entry := &target{id: id}
			for i := 0; i < 1000; i++ {
				if err := w.publish(entry, &Event{
					TargetID: id, Kind: ThresholdObserved, UsageBytes: uint64(i),
				}); err != nil {
					return 0, err
				}
			}
		}
		return 0, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var events [2]Event
	n, err := w.ReadEvents(t.Context(), events[:])
	if err != nil || n != 2 || events[0].UsageBytes != 999 || events[1].UsageBytes != 999 {
		t.Fatalf("coalesced read = %d, %v, %+v", n, err, events)
	}
	_, err = w.submit(t.Context(), func() (TargetID, error) {
		for id := TargetID(1); id <= 3; id++ {
			if err := w.publish(&target{id: id}, &Event{TargetID: id}); err != nil {
				return 0, err
			}
		}
		return 0, nil
	})
	if !errors.Is(err, ErrEventOverflow) {
		t.Fatalf("overflow = %v", err)
	}
	if err := w.Close(); !errors.Is(err, ErrEventOverflow) {
		t.Fatalf("terminal error = %v", err)
	}
}

func TestWatcherOptionsAndPaths(t *testing.T) {
	for _, percent := range []int{-1, 0, 101} {
		t.Run(fmt.Sprint(percent), func(t *testing.T) {
			_, err := openWatcher(Options{ThresholdPercent: percent}, cgroups.Unified, t.TempDir(), nil)
			if err == nil {
				t.Fatal("invalid percentage accepted")
			}
		})
	}
	w, _ := newWatchFixture(t, cgroups.Unified, 1)
	for _, path := range []string{"", "relative", "/a/../b"} {
		if _, err := w.Add(t.Context(), path); err == nil {
			t.Fatalf("invalid path accepted: %q", path)
		}
	}
	if _, err := w.ReadEvents(t.Context(), nil); err == nil {
		t.Fatal("empty destination accepted")
	}
}

func BenchmarkMemoryWatchDelivery(b *testing.B) {
	for _, count := range []int{1, 1024, 4096} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			w := &Watcher{
				options: Options{MaxCgroups: count},
				pending: make(map[TargetID]*target, count),
				ready:   make(chan struct{}, 1), done: make(chan struct{}),
			}
			targets := make([]target, count)
			for i := range targets {
				targets[i].id = TargetID(i + 1)
			}
			var dst [1]Event
			ctx := b.Context()
			i := 0
			b.ReportAllocs()
			for b.Loop() {
				entry := &targets[i%count]
				event := Event{TargetID: entry.id, Kind: ThresholdObserved}
				if err := w.publish(entry, &event); err != nil {
					b.Fatal(err)
				}
				if _, err := w.ReadEvents(ctx, dst[:]); err != nil {
					b.Fatal(err)
				}
				i++
			}
		})
	}
}
