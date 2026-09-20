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

package java

import (
	"encoding/binary"
	"testing"
)

func TestObjectSizeSlowAllocation(t *testing.T) {
	metadata := &vmMeta{constants: map[string]int64{
		"Klass::_lh_instance_slow_path_bit": 1,
		"HeapWordSize":                      8,
	}}
	for _, test := range []struct {
		name      string
		className string
		layout    int32
		want      uint64
	}{
		{name: "ordinary", className: "Payload", layout: 144, want: 144},
		{name: "finalizable", className: "FinalizablePayload", layout: 145, want: 144},
		{name: "large_finalizable", className: "LargeFinalizablePayload", layout: 1048577, want: 1048576},
		{name: "class_mirror", className: "java/lang/Class", layout: 145, want: 192},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := make([]byte, 32)
			binary.LittleEndian.PutUint32(raw[16:], 24)
			class := &klass{name: test.className, layoutHelper: test.layout}
			size, err := objectSize(raw, class, metadata, 16, 12)
			if err != nil || size != test.want {
				t.Fatalf("size = %d, %v; want %d", size, err, test.want)
			}
			if test.className == "java/lang/Class" {
				// Mirror sizes require a remote field; a missing offset must fail.
				if _, err := humongousObjectSize(processMemory{ctx: t.Context()}, 0, nil, class, metadata, 0, 12); err == nil {
					t.Fatal("accepted a mirror without its size field")
				}
				return
			}
			// Fixed-size instances must not perform a remote payload read.
			size, err = humongousObjectSize(processMemory{ctx: t.Context()}, 0, nil, class, metadata, 0, 12)
			if err != nil || size != test.want {
				t.Fatalf("humongous size = %d, %v; want %d", size, err, test.want)
			}
		})
	}
}
