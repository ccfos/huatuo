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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/pod"
)

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
