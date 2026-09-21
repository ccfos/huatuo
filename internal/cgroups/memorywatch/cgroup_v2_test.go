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
	"os"
	"path/filepath"
	"testing"

	"github.com/ccfos/huatuo/internal/cgroups"
)

func TestWatcherV2NativeCountersAndLimit(t *testing.T) {
	w, f := newWatchFixture(t, cgroups.Unified, 4)
	f.create(t, "/a", 80, 100)
	id, err := w.Add(t.Context(), "/a")
	if err != nil {
		t.Fatal(err)
	}
	f.write(t, "/a", "memory.current", "96")
	f.write(t, "/a", "memory.events", "high 0\nmax 1\n")
	event := readWatchEvent(t, w, ThresholdObserved)
	if event.TargetID != id || event.UsageBytes != 96 {
		t.Fatalf("max event = %+v", event)
	}
	high, err := os.ReadFile(filepath.Join(f.root, "a/memory.high"))
	if err != nil || string(high) != "max" {
		t.Fatalf("memory.high changed: %q, %v", high, err)
	}
	f.write(t, "/a", "memory.current", "97")
	f.write(t, "/a", "memory.events", "high 1\nmax 1\n")
	if event := readWatchEvent(t, w, ThresholdObserved); event.UsageBytes != 97 {
		t.Fatalf("high event = %+v", event)
	}
	f.write(t, "/a", "memory.current", "80")
	f.write(t, "/a", "memory.max", "85")
	if event := readWatchEvent(t, w, ThresholdObserved); event.LimitBytes != 85 {
		t.Fatalf("limit event = %+v", event)
	}
}

func TestWatcherFailedReadRetainsCounterBaseline(t *testing.T) {
	w, f := newWatchFixture(t, cgroups.Unified, 2)
	f.create(t, "/a", 80, 100)
	if _, err := w.Add(t.Context(), "/a"); err != nil {
		t.Fatal(err)
	}
	f.write(t, "/a", "memory.current", "invalid")
	f.write(t, "/a", "memory.events", "high 1\nmax 0\n")
	readWatchEvent(t, w, TargetUnavailable)
	f.write(t, "/a", "memory.current", "95")
	f.write(t, "/a", "memory.events", "high 1\nmax 0\n")
	if event := readWatchEvent(t, w, ThresholdObserved); event.UsageBytes != 95 {
		t.Fatalf("retry lost cumulative event: %+v", event)
	}
}
