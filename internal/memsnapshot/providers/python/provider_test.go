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

package python

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

func TestSnapshotResult(t *testing.T) {
	readErr := fmt.Errorf("read object: %w", os.ErrPermission)
	for _, test := range []struct {
		name       string
		input      *memsnapshot.Snapshot
		readErr    error
		wantStatus memsnapshot.Status
	}{
		{
			name: "complete", input: &memsnapshot.Snapshot{Status: memsnapshot.SnapshotStatusComplete},
			wantStatus: memsnapshot.SnapshotStatusComplete,
		},
		{
			name: "partial", input: &memsnapshot.Snapshot{
				Status: memsnapshot.SnapshotStatusPartial, StatusReason: "read budget reached",
			},
			wantStatus: memsnapshot.SnapshotStatusPartial,
		},
		{
			name: "unsupported runtime", readErr: unsupportedRuntime("unknown layout"),
			wantStatus: memsnapshot.SnapshotStatusUnavailable,
		},
		{name: "read failure", readErr: readErr},
		{name: "reader returned no snapshot"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := snapshotResult(test.input, test.readErr)
			if test.wantStatus == "" {
				if result != nil || err == nil {
					t.Fatalf("snapshot = %+v, %v, want no snapshot and an error", result, err)
				}
				if test.readErr != nil && !errors.Is(err, os.ErrPermission) {
					t.Fatalf("snapshot error lost read cause: %v", err)
				}
				return
			}
			if err != nil || result == nil || result.Status != test.wantStatus {
				t.Fatalf("snapshot = %+v, %v, want status %s", result, err, test.wantStatus)
			}
			if test.wantStatus != memsnapshot.SnapshotStatusComplete && result.StatusReason == "" {
				t.Fatal("degraded snapshot has no reason")
			}
		})
	}
}

func TestSnapshotErrorBoundsPreserveCause(t *testing.T) {
	cause := &os.PathError{
		Op: "read", Path: strings.Repeat("界", maxReasonBytes), Err: os.ErrPermission,
	}
	result, err := snapshotResult(nil, cause)
	if result != nil || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("snapshot = %+v, %v, want no snapshot and permission error", result, err)
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || pathErr != cause {
		t.Fatalf("snapshot error lost path details: %v", err)
	}
	if len(err.Error()) > maxReasonBytes || !utf8.ValidString(err.Error()) {
		t.Fatalf("invalid bounded error: %q", err.Error())
	}
}

func TestSnapshotReadFailure(t *testing.T) {
	p := New()
	result, err := p.Snapshot(t.Context(), memsnapshot.Request{
		Process:                memsnapshot.ProcessInstanceID{TGID: int(^uint32(0) >> 1), StartTimeTicks: 1},
		MaxMemoryObjectEntries: 10,
	})
	if result != nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot = %+v, %v, want no snapshot and missing process", result, err)
	}
}

func TestHeapTypeQualifiedName(t *testing.T) {
	for _, tc := range []struct {
		minor  int
		offset uint64
	}{
		{8, 864},
		{9, 856},
		{10, 864},
		{11, 864},
		{12, 872},
		{13, 872},
		{14, 872},
	} {
		t.Run(fmt.Sprintf("3.%d", tc.minor), func(t *testing.T) {
			const typeAddress, dict, keys, moduleKey, moduleValue, qualifiedName = uint64(0x10000),
				uint64(0x20000), uint64(0x30000), uint64(0x40000), uint64(0x50000), uint64(0x60000)
			layout, err := layoutFor(version{major: 3, minor: tc.minor})
			if err != nil {
				t.Fatal(err)
			}
			raw := sparseMemory{}
			put64 := func(address, value uint64) {
				var data [8]byte
				binary.LittleEndian.PutUint64(data[:], value)
				raw.put(address, data[:])
			}
			putString := func(address uint64, value string) {
				put64(address+16, uint64(len(value)))
				raw.put32(address+32, 1<<2|1<<5|1<<6)
				raw.put(address+layout.unicodeDataOffset, []byte(value))
			}
			putString(moduleKey, "__module__")
			putString(moduleValue, "app")
			putString(qualifiedName, "Outer.Inner")
			put64(dict+32, keys)
			entryOffset := uint64(48)
			keyOffset, valueOffset := uint64(8), uint64(16)
			if tc.minor >= 11 {
				raw.put(keys+8, []byte{3, 3, 1})
				put64(keys+24, 1)
				entryOffset = 40
				keyOffset, valueOffset = 0, 8
			} else {
				put64(keys+8, 8)
				put64(keys+32, 1)
			}
			put64(keys+entryOffset+keyOffset, moduleKey)
			put64(keys+entryOffset+valueOffset, moduleValue)
			put64(typeAddress+tc.offset, qualifiedName)
			c := newScanner(raw, &image{
				version: version{major: 3, minor: tc.minor},
				layout:  layout, order: binary.LittleEndian,
			}, time.Time{})
			info := typeInfo{address: typeAddress, dict: dict, name: "Inner"}
			if name := c.heapTypeName(info); name != "app.Outer.Inner" {
				t.Fatalf("name = %q, want app.Outer.Inner", name)
			}
			putString(qualifiedName, "factory.<locals>.Inner")
			if name := c.heapTypeName(info); name != "app.factory.<locals>.Inner" {
				t.Fatalf("local type name = %q, want app.factory.<locals>.Inner", name)
			}
			put64(typeAddress+tc.offset, 0)
			if name := c.heapTypeName(info); name != "app.Inner" {
				t.Fatalf("missing qualname fallback = %q, want app.Inner", name)
			}
			info.dict = 0
			if name := c.heapTypeName(info); name != "Inner" {
				t.Fatalf("missing module fallback = %q, want Inner", name)
			}
		})
	}
}
