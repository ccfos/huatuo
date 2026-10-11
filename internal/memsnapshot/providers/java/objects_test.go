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
	"context"
	"encoding/binary"
	"errors"
	"os"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
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

func TestJavaArrayDecoding(t *testing.T) {
	metadata := &vmMeta{constants: map[string]int64{
		"HeapRegionType::StartsHumongousTag":    12,
		"HeapRegionType::ContinuesHumongousTag": 13,
		"Klass::_lh_header_size_shift":          16,
		"Klass::_lh_header_size_mask":           255,
		"Klass::_lh_log2_element_size_mask":     255,
	}}
	raw := make([]byte, 24)
	binary.LittleEndian.PutUint32(raw[12:], 3)
	layout := uint32(0x80000000 | 16<<16 | 2)
	size, err := objectSize(raw, &klass{layoutHelper: int32(layout)}, metadata, 0, 12)
	if err != nil || size != 32 {
		t.Fatalf("array size: %d, %v", size, err)
	}
	if _, err := objectSize(raw[:12], &klass{layoutHelper: int32(layout)}, metadata, 0, 12); err == nil {
		t.Fatal("truncated array header accepted")
	}
}

func TestObjectMetadataReadErrors(t *testing.T) {
	data := mapTestMemory(t, 1)
	base := uint64(uintptr(unsafe.Pointer(&data[0])))
	memory := processMemory{pid: os.Getpid(), ctx: t.Context()}
	metadata := &vmMeta{compressedKlass: true, structs: map[string]vmStruct{
		"CompressedKlassPointers::_base":    {isStatic: true, address: base},
		"CompressedKlassPointers::_shift":   {isStatic: true, address: 1},
		"java_lang_Class::_oop_size_offset": {isStatic: true, address: 1},
	}}
	if _, err := pointerEncoding(memory, metadata); !errors.Is(err, unix.EFAULT) {
		t.Fatalf("shift read lost cause: %v", err)
	}
	if _, err := mirrorSizeOffset(memory, metadata); !errors.Is(err, unix.EFAULT) {
		t.Fatalf("mirror read lost cause: %v", err)
	}
	binary.LittleEndian.PutUint32(data, 4097)
	metadata.structs["CompressedKlassPointers::_shift"] = vmStruct{isStatic: true, address: base}
	metadata.structs["java_lang_Class::_oop_size_offset"] = vmStruct{isStatic: true, address: base}
	if _, err := pointerEncoding(memory, metadata); !errors.Is(err, errHotSpotUnavailable) {
		t.Fatalf("invalid shift = %v", err)
	}
	if _, err := mirrorSizeOffset(memory, metadata); !errors.Is(err, errHotSpotUnavailable) {
		t.Fatalf("invalid mirror offset = %v", err)
	}
	memory.deadline, memory.hasDeadline = time.Unix(1, 0), true
	if _, err := mirrorSizeOffset(memory, metadata); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("mirror deadline lost: %v", err)
	}
}
