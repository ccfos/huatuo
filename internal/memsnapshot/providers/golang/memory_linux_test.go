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
	"bytes"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

func TestProcessMemoryBatch(t *testing.T) {
	source := []byte("abcdefgh")
	var pin runtime.Pinner
	pin.Pin(&source[0])
	defer pin.Unpin()
	address := uint64(uintptr(unsafe.Pointer(&source[0])))
	var first, last [4]byte
	ranges := []remoteRange{
		{address: address, data: first[:]},
		{address: address + 4, data: last[:]},
	}
	memory := processMemory{pid: os.Getpid()}
	if err := memory.readBatch(ranges); err != nil || string(first[:]) != "abcd" || string(last[:]) != "efgh" {
		t.Fatalf("batch = %q, %q, err=%v", first, last, err)
	}
	clear(first[:])
	clear(last[:])
	ranges[0].address = 1
	if err := memory.readBatch(ranges); err == nil {
		t.Fatal("accepted an unreadable range after reusing the batch")
	}
	if last != ([4]byte{}) {
		t.Fatal("retried the later range after a batch failure")
	}
	if err := memory.readBatch(nil); err != nil {
		t.Fatalf("empty batch: %v", err)
	}
}

func TestProcessMemoryBatchUnreadableRange(t *testing.T) {
	source := []byte("abcd")
	var pin runtime.Pinner
	pin.Pin(&source[0])
	defer pin.Unpin()
	address := uint64(uintptr(unsafe.Pointer(&source[0])))
	for badIndex := range 3 {
		t.Run(fmt.Sprint(badIndex), func(t *testing.T) {
			var dst [3][4]byte
			ranges := make([]remoteRange, len(dst))
			for i := range ranges {
				ranges[i] = remoteRange{address: address, data: dst[i][:]}
			}
			ranges[badIndex].address = 1
			memory := processMemory{pid: os.Getpid()}
			if err := memory.readBatch(ranges); err == nil {
				t.Fatal("accepted a batch containing an unreadable range")
			}
			for i := badIndex; i < len(dst); i++ {
				if dst[i] != ([4]byte{}) {
					t.Fatalf("retried range %d after a batch failure", i)
				}
			}
		})
	}
}

func TestProcessMemoryReadRangeSizeLimit(t *testing.T) {
	source := bytes.Repeat([]byte{0x7f}, maxProcessReadBytes)
	var pin runtime.Pinner
	pin.Pin(&source[0])
	defer pin.Unpin()
	address := uint64(uintptr(unsafe.Pointer(&source[0])))
	memory := processMemory{pid: os.Getpid()}
	dst := make([]byte, 2*maxProcessReadBytes)
	ranges := []remoteRange{
		{address: address, data: dst[:maxProcessReadBytes]},
		{address: address, data: dst[maxProcessReadBytes:]},
	}
	if err := memory.readBatch(ranges); err != nil {
		t.Fatalf("the size limit must apply per range: %v", err)
	}
	for i := range ranges {
		if !bytes.Equal(ranges[i].data, source) {
			t.Fatalf("range %d was not fully read", i)
		}
	}
	ranges[1].data = dst[:maxProcessReadBytes+1]
	if err := memory.readBatch(ranges); err == nil || !strings.Contains(err.Error(), "range is invalid") {
		t.Fatalf("oversized range: %v", err)
	}
	if err := memory.readInto(address, ranges[1].data); err == nil || !strings.Contains(err.Error(), "range is invalid") {
		t.Fatalf("oversized single read: %v", err)
	}
}

func TestProcessMemoryOverflow(t *testing.T) {
	memory := processMemory{pid: os.Getpid()}
	var buffer [8]byte
	if err := memory.readInto(^uint64(0)-3, buffer[:]); err == nil {
		t.Fatal("accepted an overflowing read")
	}
	if err := memory.readBatch([]remoteRange{{address: ^uint64(0) - 3, data: buffer[:]}}); err == nil {
		t.Fatal("accepted an overflowing batch range")
	}
}

func TestProcessMemoryShortRead(t *testing.T) {
	pageSize := os.Getpagesize()
	pages, err := unix.Mmap(-1, 0, 2*pageSize, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_PRIVATE|unix.MAP_ANON)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := unix.Munmap(pages); err != nil {
			t.Error(err)
		}
	}()
	copy(pages[pageSize-4:], "tail")
	if err := unix.Mprotect(pages[pageSize:], unix.PROT_NONE); err != nil {
		t.Fatal(err)
	}
	memory := processMemory{pid: os.Getpid()}
	address := uint64(uintptr(unsafe.Pointer(&pages[pageSize-4])))
	var dst [8]byte
	if err := memory.readInto(address, dst[:]); err == nil {
		t.Fatal("accepted an incomplete read across an inaccessible page")
	}
	if err := memory.readBatch([]remoteRange{{address: address, data: dst[:]}}); err == nil {
		t.Fatal("accepted an incomplete batch read across an inaccessible page")
	}
}
