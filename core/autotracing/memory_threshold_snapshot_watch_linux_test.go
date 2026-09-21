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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/pod"
)

func TestMemoryCgroupLifecycleWakesWatcher(t *testing.T) {
	w := newTestPressureWatcher(t)
	first := "/" + strings.Repeat("a", 64)
	createMemoryCgroupForTest(t, w.root, first, 95)
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
		case <-time.After(3 * time.Second):
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
		case <-time.After(3 * time.Second):
			t.Fatal("watcher was not woken")
		}
	}
	waitPressure(first)
	second := "/" + strings.Repeat("b", 64)
	createMemoryCgroupForTest(t, w.root, second, 95)
	changes <- pod.MemoryCgroupChange{ContainerID: filepath.Base(second)}
	waitPressure(second)
}

func TestMemorySnapshotSlowConsumerDoesNotBlockLifecycle(t *testing.T) {
	w := newTestPressureWatcher(t)
	first, second := strings.Repeat("a", 64), strings.Repeat("b", 64)
	createMemoryCgroupForTest(t, w.root, "/"+first, 95)
	createMemoryCgroupForTest(t, w.root, "/"+second, 95)
	changes := make(chan pod.MemoryCgroupChange, 1)
	resolved := make(chan string, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	w.changes = changes
	w.containerPath = func(id string) (string, error) {
		select {
		case resolved <- id:
			return "/" + id, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	events, done := w.Run(ctx)
	defer cancel()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	// Leave the output full while exercising registration and removal.
	for len(events) == 0 {
		select {
		case <-ctx.Done():
			t.Fatal("initial pressure was not delivered")
		case <-time.After(time.Millisecond):
		}
	}
	third, fourth := strings.Repeat("c", 64), strings.Repeat("d", 64)
	for _, id := range []string{third, fourth} {
		createMemoryCgroupForTest(t, w.root, "/"+id, 95)
		select {
		case changes <- pod.MemoryCgroupChange{ContainerID: id}:
		case <-ctx.Done():
			t.Fatal("slow consumer blocked lifecycle submission")
		}
		select {
		case got := <-resolved:
			if got != id {
				t.Fatalf("resolved container = %s, want %s", got, id)
			}
		case <-ctx.Done():
			t.Fatal("slow consumer blocked container registration")
		}
		if id == third {
			if err := os.RemoveAll(w.memcgDir("/" + first)); err != nil {
				t.Fatal(err)
			}
			changes <- pod.MemoryCgroupChange{ContainerID: first, Removed: true}
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if w.cgroups["/"+third] == nil || w.cgroups["/"+first] != nil {
		t.Fatal("slow consumer prevented lifecycle reconciliation")
	}
}

func TestMemorySnapshotIgnoresRetiredTarget(t *testing.T) {
	w := newTestPressureWatcher(t)
	path := "/" + strings.Repeat("a", 64)
	createMemoryCgroupForTest(t, w.root, path, 95)
	if err := w.addCgroup(t.Context(), filepath.Base(path), path); err != nil {
		t.Fatal(err)
	}
	waitMemoryPressureForTest(t, w, path)
	if err := w.removeCgroup(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	if len(w.pending) != 0 {
		t.Fatal("retired target retained unread pressure")
	}
}
