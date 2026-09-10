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

package context

import (
	"runtime"
	"strconv"
	"testing"
)

func TestParseCPUIDListHonorsOnlineCPUList(t *testing.T) {
	orig := onlineCPUIDs
	defer func() { onlineCPUIDs = orig }()
	// Sparse online set with a hole: 4-7 are offline but inside the
	// 0-9 range, 8-9 are online above runtime.NumCPU().
	onlineCPUIDs = func() []int { return []int{0, 1, 2, 3, 8, 9} }

	got, err := parseCPUIDList("8,9")
	if err != nil {
		t.Fatalf("parseCPUIDList(8,9) error = %v", err)
	}
	if len(got) != 2 || got[0] != 8 || got[1] != 9 {
		t.Fatalf("parseCPUIDList(8,9) = %v, want [8 9]", got)
	}
}

// TestParseCPUIDListRejectsOfflineCPU pins the membership contract: an ID
// inside the online range can still be offline, and profiling it would fail
// perf_event_open later.
func TestParseCPUIDListRejectsOfflineCPU(t *testing.T) {
	orig := onlineCPUIDs
	defer func() { onlineCPUIDs = orig }()
	onlineCPUIDs = func() []int { return []int{0, 1, 2, 3, 8, 9} }

	err := func() error {
		_, err := parseCPUIDList("4")
		return err
	}()
	if err == nil {
		t.Fatal("parseCPUIDList(4) error = nil, want not-online error")
	}
	if want := "cpuid 4 is not online (online: 0-3,8-9)"; err.Error() != want {
		t.Fatalf("parseCPUIDList(4) error = %q, want %q", err.Error(), want)
	}

	if _, err := parseCPUIDList("4-7"); err == nil {
		t.Fatal("parseCPUIDList(4-7) error = nil, want not-online error")
	}
}

func TestParseCPUIDListRejectsBeyondOnlineBound(t *testing.T) {
	orig := onlineCPUIDs
	defer func() { onlineCPUIDs = orig }()
	onlineCPUIDs = func() []int { return []int{0, 1, 2, 3, 8, 9} }

	if _, err := parseCPUIDList("10"); err == nil {
		t.Fatal("parseCPUIDList(10) error = nil, want out-of-range error")
	}
}

func TestParseCPUIDListFallbackToNumCPUWhenSysfsUnavailable(t *testing.T) {
	orig := onlineCPUIDs
	defer func() { onlineCPUIDs = orig }()
	onlineCPUIDs = func() []int { return nil }

	highest := strconv.Itoa(runtime.NumCPU() - 1)
	got, err := parseCPUIDList(highest)
	if err != nil {
		t.Fatalf("parseCPUIDList(%q) error = %v", highest, err)
	}
	if len(got) != 1 || got[0] != runtime.NumCPU()-1 {
		t.Fatalf("parseCPUIDList(%q) = %v, want [%d]", highest, got, runtime.NumCPU()-1)
	}

	if _, err := parseCPUIDList(strconv.Itoa(runtime.NumCPU())); err == nil {
		t.Fatal("parseCPUIDList(NumCPU) error = nil, want out-of-range error")
	}
}
