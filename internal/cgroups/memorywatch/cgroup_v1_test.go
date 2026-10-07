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
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/stats"
)

func TestWatcherV1EventFDAndRearm(t *testing.T) {
	before, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	w, f := newWatchFixture(t, cgroups.Legacy, 4)
	f.create(t, "/a", 80, 100)
	id, err := w.Add(t.Context(), "/a", f.stat(t, "/a"))
	if err != nil {
		t.Fatal(err)
	}
	control := filepath.Join(f.root, "a/cgroup.event_control")
	readRegistration := func() (int, uint64) {
		t.Helper()
		data, err := os.ReadFile(control)
		if err != nil {
			t.Fatal(err)
		}
		var fd, usageFD int
		var threshold uint64
		if _, err := fmt.Sscanf(string(data), "%d %d %d", &fd, &usageFD, &threshold); err != nil {
			t.Fatal(err)
		}
		return fd, threshold
	}
	fd, threshold := readRegistration()
	if threshold != 90 {
		t.Fatalf("threshold = %d", threshold)
	}
	f.write(t, "/a", "memory.usage_in_bytes", "95")
	signalEventFD(fd)
	if event := readWatchEvent(t, w, ThresholdObserved); event.TargetID != id {
		t.Fatalf("event = %+v", event)
	}
	// Unlike cgroupfs, truncating a regular fixture briefly exposes an empty
	// limit. Overwrite the same byte count to model one valid kernel write.
	limit, err := os.OpenFile(filepath.Join(f.root, "a/memory.limit_in_bytes"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := limit.WriteAt([]byte("80\n"), 0)
	closeErr := limit.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("update limit: write %v, close %v", writeErr, closeErr)
	}
	readWatchEvent(t, w, ThresholdObserved)
	_, threshold = readRegistration()
	if threshold != 72 {
		t.Fatalf("rearmed threshold = %d, want 72", threshold)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("rearm leaked FDs: before %d, after close %d", len(before), len(after))
	}
}

func TestWatcherV1ChecksUsageAfterRegistration(t *testing.T) {
	f := &watchFixture{root: t.TempDir(), mode: cgroups.Legacy}
	f.create(t, "/a", 80, 100)
	reads := 0
	w, err := openWatcher(Options{ThresholdPercent: 90}, cgroups.Legacy, f.root,
		func(string) (*stats.MemoryUsage, error) {
			reads++
			usage := uint64(80)
			if reads > 1 {
				usage = 95
			}
			return &stats.MemoryUsage{Usage: usage, MaxLimited: 100}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	id, err := w.Add(t.Context(), "/a", f.stat(t, "/a"))
	if err != nil {
		t.Fatal(err)
	}
	// No eventfd signal: the registration baseline can already be above threshold.
	if event := readWatchEvent(t, w, ThresholdObserved); event.TargetID != id || event.UsageBytes != 95 {
		t.Fatalf("post-registration observation = %+v", event)
	}
}
