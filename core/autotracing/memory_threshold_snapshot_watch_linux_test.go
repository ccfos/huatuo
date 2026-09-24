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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/cgroups/memorywatch"
)

func newTestMemoryThresholdWatcher(t testing.TB, opts memoryWatchOptions) (*memoryThresholdWatcher, *cgroupSource) {
	t.Helper()
	source := newTestCgroupSource(t)
	watcher, err := newMemoryThresholdWatcher(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := watcher.Close(); err != nil {
			t.Error(err)
		}
	})
	return watcher, source
}

func TestMemoryThresholdWatcherRegistersPaths(t *testing.T) {
	watcher, source := newTestMemoryThresholdWatcher(t, memoryWatchOptions{ThresholdPercent: 90})
	// A plain hierarchy path needs neither a container ID nor a pod subscription.
	path := "/workload"
	createMemoryCgroupForTest(t, source.root, path, 95)
	identity := cgroupRefForTest(t, source, path).directory
	first, err := watcher.Register(t.Context(), path, identity)
	if err != nil || first == 0 {
		t.Fatalf("register path = %d, %v", first, err)
	}
	duplicate, err := watcher.Register(t.Context(), path, identity)
	if err != nil || duplicate != first {
		t.Fatalf("duplicate registration = %d, %v; want %d", duplicate, err, first)
	}
	if err := os.Rename(source.memcgDir(path), filepath.Join(source.root, "retired")); err != nil {
		t.Fatal(err)
	}
	createMemoryCgroupForTest(t, source.root, path, 99)
	replacement, err := watcher.Register(t.Context(), path, cgroupRefForTest(t, source, path).directory)
	if err != nil || replacement == first {
		t.Fatalf("replacement registration = %d, %v", replacement, err)
	}
	if staleID, err := watcher.Register(t.Context(), path, identity); staleID != 0 || !errors.Is(err, unix.ESTALE) {
		t.Fatalf("stale identity reused the replacement registration: %d, %v", staleID, err)
	}
	if err := watcher.Unregister(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	events, err := watcher.ProcessEvents(t.Context())
	if err != nil || len(events) != 1 {
		t.Fatalf("replacement events = %+v, %v", events, err)
	}
	event := &events[0]
	if event.TargetID != replacement || event.Kind != memoryThresholdObserved || event.UsageBytes != 99 {
		t.Fatalf("replacement observation = %+v", event)
	}
	retained := slices.Clone(events)
	if err := watcher.Unregister(t.Context(), replacement); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(events, retained) {
		t.Fatal("unregister changed a borrowed kernel batch")
	}
	remaining, err := watcher.ProcessEvents(t.Context())
	if err != nil || len(remaining) != 0 {
		t.Fatalf("unregistered events = %+v, %v", remaining, err)
	}
}

func TestMemoryThresholdWatcherPressureBatchBudget(t *testing.T) {
	watcher, source := newTestMemoryThresholdWatcher(t, memoryWatchOptions{ThresholdPercent: 90})
	const count = 2*defaultMemoryWatchEventBatch + 1
	for i := 0; i < count; i++ {
		path := fmt.Sprintf("/workload-%d", i)
		createMemoryCgroupForTest(t, source.root, path, 95)
		if _, err := watcher.Register(t.Context(), path, cgroupRefForTest(t, source, path).directory); err != nil {
			t.Fatal(err)
		}
	}
	seen := make(map[memoryWatchRegistrationID]bool)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for len(seen) < count {
		select {
		case <-watcher.Notifications():
		case <-ctx.Done():
			t.Fatal("remaining kernel events lost their notification")
		}
		events, err := watcher.ProcessEvents(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) > defaultMemoryWatchEventBatch || cap(events) != len(events) {
			t.Fatalf("unbounded kernel batch: len=%d cap=%d", len(events), cap(events))
		}
		for i := range events {
			if seen[events[i].TargetID] || events[i].Kind != memoryThresholdObserved {
				t.Fatalf("duplicate or unexpected event: %+v", events[i])
			}
			seen[events[i].TargetID] = true
		}
	}
}

func TestMemoryThresholdWatcherRegistrationLimit(t *testing.T) {
	watcher, source := newTestMemoryThresholdWatcher(t, memoryWatchOptions{ThresholdPercent: 90, MaxCgroups: 1})
	for _, path := range []string{"/first", "/second"} {
		createMemoryCgroupForTest(t, source.root, path, 95)
	}
	first, err := watcher.Register(t.Context(), "/first", cgroupRefForTest(t, source, "/first").directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := watcher.Register(t.Context(), "/second", cgroupRefForTest(t, source, "/second").directory); !errors.Is(err, memorywatch.ErrTargetLimit) {
		t.Fatalf("registration limit lost its cause: %v", err)
	}
	if err := watcher.Unregister(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if _, err := watcher.Register(t.Context(), "/second", cgroupRefForTest(t, source, "/second").directory); err != nil {
		t.Fatalf("unregister did not release capacity: %v", err)
	}
}

func TestMemoryThresholdWatcherClosedAndCanceled(t *testing.T) {
	watcher, _ := newTestMemoryThresholdWatcher(t, memoryWatchOptions{ThresholdPercent: 90})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := watcher.ProcessEvents(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled processing = %v", err)
	}
	notifications := watcher.Notifications()
	for i := 0; i < 2; i++ {
		if err := watcher.Close(); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case _, ok := <-notifications:
		if ok {
			t.Fatal("idle closed watcher retained a notification")
		}
	default:
		t.Fatal("watcher close did not finish the notification producer")
	}
	if _, err := watcher.ProcessEvents(t.Context()); !errors.Is(err, memorywatch.ErrClosed) {
		t.Fatalf("processing after close lost its cause: %v", err)
	}
}
