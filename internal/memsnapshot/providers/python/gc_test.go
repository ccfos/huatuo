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
	"context"
	"encoding/binary"
	"fmt"
	"testing"
	"time"
)

func TestGCGenerationAlignment(t *testing.T) {
	for _, minor := range []int{12, 13, 14} {
		for _, offset := range []uint64{20, 24} {
			for _, populated := range []bool{false, true} {
				t.Run(fmt.Sprintf("3.%d/offset%d/populated%v", minor, offset, populated), func(t *testing.T) {
					const interpreter, gcOffset = uint64(0x10000), uint64(0x100)
					raw := sparseMemory{}
					put64 := func(address, value uint64) {
						var data [8]byte
						binary.LittleEndian.PutUint64(data[:], value)
						raw.put(address, data[:])
					}
					first := interpreter + gcOffset + offset
					// generation0 is an eight-byte-aligned pointer in 3.12/3.13;
					// 3.14 places the permanent generation directly after the array.
					permanent := interpreter + gcOffset + 104
					if minor == 14 {
						permanent = first + 72
					}
					heads := [4]uint64{first, first + 24, first + 48, permanent}
					for i, head := range heads {
						put64(head, head)
						put64(head+8, head)
						raw.put32(head+16, 700)
						if populated {
							node := uint64(0x20000 + i*0x100)
							put64(head, node)
							put64(head+8, node)
							put64(node, head)
							put64(node+8, head)
						}
					}
					c := newScanner(raw, &image{
						order:   binary.LittleEndian,
						version: version{major: 3, minor: minor},
					}, time.Time{})
					interpreters, err := c.interpreterList(interpreter, 0, gcOffset)
					if err != nil {
						t.Fatal(err)
					}
					if len(interpreters) != 1 || interpreters[0].heads != heads {
						t.Fatalf("heads=%v, want %v", interpreters, heads)
					}
					for _, head := range heads {
						if !c.walkGeneration(context.Background(), head) {
							t.Fatal(c.partial)
						}
					}
					if populated && c.scannedObjects != 4 {
						t.Fatalf("scanned=%d, want 4", c.scannedObjects)
					}
					// A four-byte-aligned sentinel must not permit an invalid object link.
					put64(first, 0x20004)
					if _, err := c.interpreterList(interpreter, 0, gcOffset); err == nil {
						t.Fatal("accepted a broken generation list")
					}
				})
			}
		}
	}
}
