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

package golang

import (
	"debug/elf"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestNewRuntimeLayoutVersions(t *testing.T) {
	for _, tc := range []struct {
		version string
		valid   bool
	}{
		{"go1.18", true},
		{"go1.26.4", true},
		{"go1.26rc1", true},
		{"go1.26.0-custom", true},
		{"go1.17.13", false},
		{"go1.27.0", false},
		{"invalid-go1.26.0", false},
		{"go1.26.0 garbage", false},
		{"devel go1.26-abcdef", false},
		{"", false},
	} {
		t.Run(tc.version, func(t *testing.T) {
			_, err := newRuntimeLayout(tc.version, elf.ELFCLASS64, binary.LittleEndian)
			if (err == nil) != tc.valid {
				t.Fatalf("validate %q: %v", tc.version, err)
			}
			if err != nil && !errors.Is(err, errUnsupportedRuntime) {
				t.Fatalf("error category: %v", err)
			}
		})
	}
}

func TestNewRuntimeLayoutBoundaries(t *testing.T) {
	for minor := 18; minor <= 26; minor++ {
		version := fmt.Sprintf("go1.%d", minor)
		for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
			layout, err := newRuntimeLayout(version, elf.ELFCLASS64, order)
			want := uint64(32)
			if minor >= 23 {
				want = 1024
			}
			if err != nil || layout.maxStackDepth != want || layout.byteOrder != order {
				t.Fatalf("layout %s: %+v, %v", version, layout, err)
			}
		}
	}
	for _, class := range []elf.Class{elf.ELFCLASSNONE, elf.ELFCLASS32, elf.Class(255)} {
		_, err := newRuntimeLayout("go1.26", class, binary.LittleEndian)
		if !errors.Is(err, errUnsupportedRuntime) {
			t.Fatalf("class %v: %v", class, err)
		}
		if !strings.Contains(err.Error(), "ELF class "+class.String()) ||
			!strings.Contains(err.Error(), "only ELFCLASS64 (64-bit) is supported") {
			t.Fatalf("class %v error must identify the target and supported class: %v", class, err)
		}
	}
}

func TestRuntimeLayoutDecodeBucketHeader(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		for _, limit := range []uint64{32, 1024} {
			layout := runtimeLayout{byteOrder: order, maxStackDepth: limit}
			for _, depth := range []uint64{0, limit, limit + 1, math.MaxUint64} {
				var header bucketHeader
				order.PutUint64(header.raw[0:8], 99) // Hash link must not be used for traversal.
				order.PutUint64(header.raw[8:16], 123)
				order.PutUint64(header.raw[16:24], 1)
				order.PutUint64(header.raw[40:48], depth)
				got, err := layout.decodeBucketHeader(4096, &header)
				if depth > limit {
					if err == nil {
						t.Fatalf("accepted depth %d with limit %d", depth, limit)
					}
					continue
				}
				want := bucketDescriptor{nextAddr: 123, stackAddr: 4144, recordAddr: 4144 + depth*8, stackDepth: int(depth)}
				if err != nil || got != want {
					t.Fatalf("descriptor: %+v, %v; want %+v", got, err, want)
				}
			}
		}
		layout := runtimeLayout{byteOrder: order, maxStackDepth: 1024}
		var header bucketHeader
		for _, typ := range []uint64{0, 2, 3, math.MaxUint64} {
			order.PutUint64(header.raw[16:24], typ)
			if _, err := layout.decodeBucketHeader(4096, &header); err == nil {
				t.Fatalf("accepted type %d", typ)
			}
		}
		order.PutUint64(header.raw[16:24], 1)
		order.PutUint64(header.raw[40:48], 1)
		lastOffset := uint64(bucketHeaderBytes + programCounterBytes + heapProfileRecordBytes - 1)
		addr := uint64(math.MaxUint64) - lastOffset
		got, err := layout.decodeBucketHeader(addr, &header)
		if err != nil || got.nextAddr != 0 || got.recordAddr+heapProfileRecordBytes-1 != math.MaxUint64 {
			t.Fatalf("last byte boundary: %+v %v", got, err)
		}
		if _, err := layout.decodeBucketHeader(addr+1, &header); err == nil {
			t.Fatal("accepted record-end overflow")
		}
	}
}

func TestRuntimeLayoutDecodeCounters(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		for _, test := range []struct {
			name           string
			cycles         [][4]uint64 // alloc objects, free objects, alloc bytes, free bytes
			objects, bytes uint64
			published      bool
		}{
			{name: "zero"},
			{name: "in use", cycles: [][4]uint64{{8, 3, 800, 300}}, objects: 5, bytes: 500, published: true},
			{name: "all freed", cycles: [][4]uint64{{8, 8, 800, 800}}, published: true},
			{name: "object underflow", cycles: [][4]uint64{{2, 3, 800, 300}}, bytes: 500, published: true},
			{name: "byte underflow", cycles: [][4]uint64{{8, 3, 200, 300}}, objects: 5, published: true},
			{name: "only free counters", cycles: [][4]uint64{{0, 3, 0, 300}}, published: true},
			{name: "only byte counters", cycles: [][4]uint64{{0, 0, 128, 0}}, bytes: 128, published: true},
			{
				name: "future counters ignored",
				cycles: [][4]uint64{
					{8, 3, 800, 300}, {0, 3, 0, 300}, {4, 1, 640, 128}, {math.MaxUint64, 2, math.MaxUint64, 256},
				},
				objects: 5, bytes: 500, published: true,
			},
			{
				name:    "large counters",
				cycles:  [][4]uint64{{math.MaxUint64, math.MaxUint64 - 1, math.MaxUint64 - 1, math.MaxUint64 - 2}},
				objects: 1, bytes: 1, published: true,
			},
			{
				name: "future only",
				cycles: [][4]uint64{
					{}, {5, 1, 640, 128}, {2, 1, 256, 128}, {1, 0, 128, 0},
				},
			},
			{
				name: "all freed with future allocations",
				cycles: [][4]uint64{
					{8, 8, 800, 800}, {4, 0, 640, 0},
				},
				published: true,
			},
		} {
			t.Run(fmt.Sprintf("%s/%s", order, test.name), func(t *testing.T) {
				var raw [heapProfileRecordBytes]byte
				for cycle, counts := range test.cycles {
					for field, value := range counts {
						order.PutUint64(raw[cycle*32+field*8:], value)
					}
				}
				layout := runtimeLayout{byteOrder: order}
				objects, bytes, published := layout.decodeCounters(&raw)
				if objects != test.objects || bytes != test.bytes || published != test.published {
					t.Fatalf("active: %d objects, %d bytes, published=%v; want %d objects, %d bytes, published=%v",
						objects, bytes, published, test.objects, test.bytes, test.published)
				}
			})
		}
	}
}
