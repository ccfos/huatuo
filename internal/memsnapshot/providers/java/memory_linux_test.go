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
	"os"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

func mapTestMemory(t *testing.T, pages int) []byte {
	t.Helper()
	data, err := unix.Mmap(-1, 0, pages*os.Getpagesize(), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_PRIVATE|unix.MAP_ANON)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := unix.Munmap(data); err != nil {
			t.Error(err)
		}
	})
	return data
}

func TestCStringAtPageBoundary(t *testing.T) {
	for _, test := range []struct {
		name, value  string
		readableNext bool
		wantError    bool
	}{
		{name: "terminated before protected page", value: "ok"},
		{name: "continues in readable page", value: "longer", readableNext: true},
		{name: "continues in protected page", value: "longer", wantError: true},
		{name: "exceeds string budget", value: strings.Repeat("a", maxHotSpotStringBytes), readableNext: true, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := mapTestMemory(t, 3)
			page := os.Getpagesize()
			copy(data[page-3:], test.value+"\x00")
			if !test.readableNext {
				if err := unix.Mprotect(data[page:], unix.PROT_NONE); err != nil {
					t.Fatal(err)
				}
			}
			memory := processMemory{pid: os.Getpid(), ctx: t.Context()}
			got, err := memory.cstring(uint64(uintptr(unsafe.Pointer(&data[page-3]))))
			if test.wantError {
				if err == nil {
					t.Fatal("invalid string accepted")
				}
				return
			}
			if err != nil || got != test.value {
				t.Fatalf("string = %q, %v; want %q", got, err, test.value)
			}
		})
	}
}
