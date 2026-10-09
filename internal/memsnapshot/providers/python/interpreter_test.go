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
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

func TestProbedInterpreterDiscovery(t *testing.T) {
	const runtimeAddress, interpreter, missing = uint64(0x10000), uint64(0x20000), uint64(0x30000)
	for _, test := range []struct {
		name          string
		next          uint64
		rejectedFirst bool
		wantError     bool
	}{
		{name: "complete"},
		{name: "unrelated candidate", rejectedFirst: true},
		{name: "unreadable successor", next: missing, wantError: true},
		{name: "previously rejected successor", next: missing, rejectedFirst: true, wantError: true},
		{name: "misaligned successor", next: missing + 1, wantError: true},
		{name: "cycle", next: interpreter, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := sparseMemory{}
			put64 := func(address, value uint64) {
				var data [8]byte
				binary.LittleEndian.PutUint64(data[:], value)
				raw.put(address, data[:])
			}
			if test.rejectedFirst {
				put64(runtimeAddress, missing)
			}
			put64(runtimeAddress+8, interpreter)
			// A second reference to the same confirmed chain is not a cycle.
			put64(runtimeAddress+16, interpreter)
			put64(interpreter, test.next)
			layout, err := layoutFor(version{major: 3, minor: 11})
			if err != nil {
				t.Fatal(err)
			}
			census := newScanner(raw, &image{runtimeAddress: runtimeAddress, order: binary.LittleEndian, version: version{major: 3, minor: 11}, layout: layout}, time.Time{})
			heads := census.generationHeads(interpreter + 0x100)
			for _, head := range heads.heads {
				put64(head, head)
				put64(head+8, head)
				raw.put32(head+16, 700)
			}
			result, err := census.snapshot(t.Context(), 10)
			if test.wantError {
				if err == nil || result != nil {
					t.Fatalf("incomplete chain = %+v, %v; want no snapshot and error", result, err)
				}
			} else if err != nil || result.Status != memsnapshot.SnapshotStatusComplete {
				t.Fatalf("complete chain = %+v, %v", result, err)
			}
		})
	}
}
