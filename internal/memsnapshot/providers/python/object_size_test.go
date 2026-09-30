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
)

func TestObjectSizeUsesRuntimeListMetadataOffset(t *testing.T) {
	for _, test := range []struct {
		name       string
		typeOffset uint64
		basicsize  int64
	}{
		{name: "standard", typeOffset: 8, basicsize: 40},
		{name: "Py_TRACE_REFS", typeOffset: 24, basicsize: 56},
	} {
		t.Run(test.name, func(t *testing.T) {
			const address = uint64(0x10000)
			memory := sparseMemory{}
			var raw [16]byte
			binary.LittleEndian.PutUint64(raw[:8], 0x20000)
			binary.LittleEndian.PutUint64(raw[8:], 4)
			memory.put(address+test.typeOffset+16, raw[:])
			objectHead := make([]byte, test.typeOffset+16)
			binary.LittleEndian.PutUint64(objectHead[test.typeOffset+8:], 3)

			scanner := newScanner(memory, &image{
				order: binary.LittleEndian,
				layout: runtimeLayout{
					objectTypeOffset: test.typeOffset,
					objectSizeOffset: test.typeOffset + 8,
				},
			}, time.Time{})
			got := scanner.objectSize(address, objectHead, typeInfo{
				name:      "list",
				basicsize: test.basicsize,
			})
			want := pyGCHeadSize + uint64(test.basicsize) + 4*8
			if got != want {
				t.Fatalf("object size = %d, want %d", got, want)
			}
		})
	}
}
