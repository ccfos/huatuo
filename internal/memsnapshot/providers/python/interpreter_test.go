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
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

func TestProbedInterpreterDiscovery(t *testing.T) {
	const runtimeAddress, interpreter, successor = uint64(0x10000), uint64(0x20000), uint64(0x30000)
	for _, runtime := range []struct {
		name          string
		minor         int
		runtimeOffset uint64
	}{
		{
			name:          "python39",
			minor:         9,
			runtimeOffset: 16,
		},
		{
			name:          "python310",
			minor:         10,
			runtimeOffset: 16,
		},
		{
			name:          "python311",
			minor:         11,
			runtimeOffset: 40,
		},
	} {
		t.Run(runtime.name, func(t *testing.T) {
			for _, test := range []struct {
				name             string
				next             uint64
				rejectedFirst    bool
				aliasSlot        uint64
				successorRuntime uint64
				wantError        bool
				readFailure      bool
				wantInterpreters int
			}{
				{
					name:             "complete",
					wantInterpreters: 1,
				},
				{
					name:             "unrelated candidate",
					rejectedFirst:    true,
					wantInterpreters: 1,
				},
				{
					name:             "alias before real interpreter",
					aliasSlot:        1,
					wantInterpreters: 1,
				},
				{
					name:             "alias after real interpreter",
					aliasSlot:        2,
					wantInterpreters: 1,
				},
				{
					name:             "wrong runtime candidate",
					rejectedFirst:    true,
					successorRuntime: runtimeAddress + 8,
					wantInterpreters: 1,
				},
				{
					name:             "multiple interpreters",
					next:             successor,
					successorRuntime: runtimeAddress,
					wantInterpreters: 2,
				},
				{
					name:             "wrong runtime successor",
					next:             successor,
					successorRuntime: runtimeAddress + 8,
					wantError:        true,
				},
				{
					name:        "unreadable successor",
					next:        successor,
					readFailure: true,
					wantError:   true,
				},
				{
					name:          "previously rejected successor",
					next:          successor,
					readFailure:   true,
					rejectedFirst: true,
					wantError:     true,
				},
				{
					name:      "misaligned successor",
					next:      successor + 1,
					wantError: true,
				},
				{
					name:      "cycle",
					next:      interpreter,
					wantError: true,
				},
			} {
				t.Run(test.name, func(t *testing.T) {
					raw := sparseMemory{}
					put64 := func(address, value uint64) {
						var data [8]byte
						binary.LittleEndian.PutUint64(data[:], value)
						raw.put(address, data[:])
					}
					if test.rejectedFirst {
						put64(runtimeAddress, successor)
					}
					put64(runtimeAddress+8, interpreter)
					// Repeated references must not duplicate the same interpreter.
					put64(runtimeAddress+16, interpreter)
					if test.aliasSlot != 0 {
						slot := runtimeAddress
						if test.aliasSlot == 2 {
							slot += 24
						}
						put64(slot, interpreter-8)
						// A neighboring allocation exposes the same GC heads with a bogus next link.
						put64(interpreter-8, 0x1621)
					}
					layout, err := layoutFor(version{major: 3, minor: runtime.minor})
					if err != nil {
						t.Fatal(err)
					}
					readErr := errors.New("interpreter fixture read failed")
					reader := &interpreterErrorMemory{memoryReader: raw, address: successor, err: readErr, enabled: test.readFailure}
					census := newScanner(reader, &image{
						runtimeAddress: runtimeAddress,
						order:          binary.LittleEndian,
						version: version{
							major: 3,
							minor: runtime.minor,
						},
						layout: layout,
					}, time.Time{})
					putInterpreter := func(address, next, owner uint64) {
						put64(address, next)
						put64(address+runtime.runtimeOffset, owner)
						heads := census.generationHeads(address + 0x100)
						for _, head := range heads.heads {
							put64(head, head)
							put64(head+8, head)
							raw.put32(head+16, 700)
						}
					}
					putInterpreter(interpreter, test.next, runtimeAddress)
					if test.successorRuntime != 0 {
						putInterpreter(successor, 0, test.successorRuntime)
					}
					result, err := census.snapshot(t.Context(), 10)
					if test.readFailure && reader.failures == 0 {
						t.Fatal("expected a failed successor read")
					}
					if test.readFailure && !test.rejectedFirst && !errors.Is(err, readErr) {
						t.Fatalf("error=%v, want underlying read error %v", err, readErr)
					}
					if test.wantError {
						if err == nil || result != nil {
							t.Fatalf("incomplete chain = %+v, %v; want no snapshot and error", result, err)
						}
					} else {
						if err != nil || result.Status != memsnapshot.SnapshotStatusComplete {
							t.Fatalf("complete chain = %+v, %v", result, err)
						}
						interpreters, err := census.findInterpreters()
						if err != nil || len(interpreters) != test.wantInterpreters {
							t.Fatalf("interpreters = %v, %v; want %d", interpreters, err, test.wantInterpreters)
						}
					}
				})
			}
		})
	}
}

// Fail only the selected address so a candidate failure remains distinguishable
// from a damaged successor of an already confirmed interpreter.
type interpreterErrorMemory struct {
	memoryReader
	address  uint64
	err      error
	enabled  bool
	failures int
}

func (m *interpreterErrorMemory) read(address uint64, size int) ([]byte, error) {
	if m.enabled && address == m.address {
		m.failures++
		return nil, m.err
	}
	return m.memoryReader.read(address, size)
}
