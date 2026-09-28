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

func TestJavaRegionGrouping(t *testing.T) {
	metadata := &vmMeta{constants: map[string]int64{
		"HeapRegionType::StartsHumongousTag":    12,
		"HeapRegionType::ContinuesHumongousTag": 13,
		"Klass::_lh_header_size_shift":          16,
		"Klass::_lh_header_size_mask":           255,
		"Klass::_lh_log2_element_size_mask":     255,
	}}
	regions := []region{
		{bottom: 4096, top: 8192, capacity: 4096, tag: 12, hasTag: true},
		{bottom: 8192, top: 9000, capacity: 4096, tag: 13, hasTag: true},
	}
	heap, err := groupRegions(regions, metadata)
	if err != nil || len(heap.humongous) != 1 || len(heap.humongous[0].regions) != 2 {
		t.Fatalf("humongous grouping: %+v, %v", heap, err)
	}
	if _, err := groupRegions(regions[1:], metadata); !errors.Is(err, errHotSpotUnavailable) {
		t.Fatalf("orphan continuation accepted: %v", err)
	}
}

func TestRegionReadPreservesDeadline(t *testing.T) {
	memory := processMemory{pid: os.Getpid(), ctx: t.Context(), deadline: time.Unix(1, 0), hasDeadline: true}
	metadata := &vmMeta{structs: map[string]vmStruct{
		"G1CollectedHeap::_hrm":         {},
		"G1HeapRegionManager::_regions": {typeString: "G1HeapRegionTable"},
		"G1HeapRegionTable::_base":      {},
		"G1HeapRegionTable::_length":    {},
	}}
	_, err := readRegionsFromHeap(memory, metadata, 0x10000)
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errHotSpotUnavailable) {
		t.Fatalf("soft deadline misclassified: %v", err)
	}
}

func TestRegionTableReadErrors(t *testing.T) {
	for _, test := range []struct {
		name                                   string
		baseOffset, lengthOffset, base, length uint64
		cause                                  error
	}{
		{"base read fails", 4096, 8, 8, 1, unix.EFAULT},
		{"length read fails", 0, 4096, 8, 1, unix.EFAULT},
		{"base is zero", 0, 8, 0, 1, errHotSpotUnavailable},
		{"length is zero", 0, 8, 8, 0, errHotSpotUnavailable},
		{"base address overflows", ^uint64(0), 8, 8, 1, errHotSpotUnavailable},
		{"length address overflows", 0, ^uint64(0), 8, 1, errHotSpotUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := mapTestMemory(t, 2)
			page := os.Getpagesize()
			if err := unix.Mprotect(data[page:], unix.PROT_NONE); err != nil {
				t.Fatal(err)
			}
			binary.LittleEndian.PutUint64(data, test.base)
			binary.LittleEndian.PutUint64(data[8:], test.length)
			baseOffset, lengthOffset := test.baseOffset, test.lengthOffset
			if baseOffset == 4096 {
				baseOffset = uint64(page)
			}
			if lengthOffset == 4096 {
				lengthOffset = uint64(page)
			}
			metadata := &vmMeta{structs: map[string]vmStruct{
				"G1CollectedHeap::_hrm": {}, "G1HeapRegionManager::_regions": {typeString: "G1HeapRegionTable"},
				"G1HeapRegionTable::_base": {offset: baseOffset}, "G1HeapRegionTable::_length": {offset: lengthOffset},
			}}
			_, err := readRegionsFromHeap(processMemory{pid: os.Getpid(), ctx: t.Context()}, metadata, uint64(uintptr(unsafe.Pointer(&data[0]))))
			if !errors.Is(err, test.cause) {
				t.Fatalf("error = %v, want %v", err, test.cause)
			}
			if errors.Is(test.cause, unix.EFAULT) && errors.Is(err, errHotSpotUnavailable) {
				t.Fatalf("read error classified as unsupported: %v", err)
			}
		})
	}
}
