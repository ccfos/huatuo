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
	"errors"
	"os"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

func TestRuntimeFlagOffsetBounds(t *testing.T) {
	for _, test := range []struct {
		name                              string
		nameOffset, addressOffset, stride uint64
	}{
		{"name wraps", ^uint64(0) - 3, 8, 16},
		{"address wraps", 0, ^uint64(0) - 3, 16},
		{"short stride", 0, 0, 7},
		{"name crosses entry", 12, 8, 16},
		{"address crosses entry", 0, 12, 16},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry := make([]byte, 16)
			fields := []uint64{uint64(uintptr(unsafe.Pointer(&entry[0]))), 1}
			metadata := &vmMeta{structs: map[string]vmStruct{
				"JVMFlag::_name": {offset: test.nameOffset}, "JVMFlag::_addr": {offset: test.addressOffset},
				"JVMFlag::flags":    {isStatic: true, address: uint64(uintptr(unsafe.Pointer(&fields[0])))},
				"JVMFlag::numFlags": {isStatic: true, address: uint64(uintptr(unsafe.Pointer(&fields[1])))},
			}, types: map[string]vmType{"JVMFlag": {size: test.stride}}}
			_, err := metadata.runtimeFlagAddresses(processMemory{pid: os.Getpid(), ctx: t.Context()}, map[string]struct{}{"x": {}})
			runtime.KeepAlive(entry)
			runtime.KeepAlive(fields)
			if !errors.Is(err, errHotSpotUnavailable) {
				t.Fatalf("invalid layout = %v", err)
			}
		})
	}
}

func TestRuntimeFlagNameAtPageBoundary(t *testing.T) {
	for _, crossesPage := range []bool{false, true} {
		t.Run(map[bool]string{false: "ends at page", true: "crosses page"}[crossesPage], func(t *testing.T) {
			data := mapTestMemory(t, 2)
			page := os.Getpagesize()
			name := "UseG1GC"
			start := page - len(name) - 1
			if crossesPage {
				start = page - 3
			}
			copy(data[start:], name+"\x00")
			base := uint64(uintptr(unsafe.Pointer(&data[0])))
			// Static table pointer, count, then one name/address record.
			binary.LittleEndian.PutUint64(data, base+16)
			binary.LittleEndian.PutUint64(data[8:], 1)
			binary.LittleEndian.PutUint64(data[16:], base+uint64(start))
			binary.LittleEndian.PutUint64(data[24:], base+32)
			if !crossesPage {
				if err := unix.Mprotect(data[page:], unix.PROT_NONE); err != nil {
					t.Fatal(err)
				}
			}
			metadata := &vmMeta{structs: map[string]vmStruct{
				"JVMFlag::_name": {}, "JVMFlag::_addr": {offset: 8},
				"JVMFlag::flags": {isStatic: true, address: base}, "JVMFlag::numFlags": {isStatic: true, address: base + 8},
			}, types: map[string]vmType{"JVMFlag": {size: 16}}}
			got, err := metadata.runtimeFlagAddresses(processMemory{pid: os.Getpid(), ctx: t.Context()}, map[string]struct{}{name: {}})
			if err != nil || got[name] != base+32 {
				t.Fatalf("flag = %v, %v", got, err)
			}
		})
	}
}
