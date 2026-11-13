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

package memsnapshot

import (
	"debug/elf"
	"os"
	"testing"
)

func TestFindELFLoadBiasMappingIdentity(t *testing.T) {
	pageSize := uint64(os.Getpagesize())
	file := &elf.File{Progs: []*elf.Prog{
		{ProgHeader: elf.ProgHeader{Type: elf.PT_LOAD, Vaddr: pageSize, Filesz: pageSize}},
	}}
	target := ProcMap{Inode: 42, DevMajor: 8, DevMinor: 3}
	maps := []ProcMap{
		{Inode: 42, DevMajor: 9, DevMinor: 3, Start: 0x100000},
		{Inode: 42, DevMajor: 8, DevMinor: 2, Start: 0x200000},
		{Inode: 41, DevMajor: 8, DevMinor: 3, Start: 0x300000},
		{Inode: 42, DevMajor: 8, DevMinor: 3, Start: 0x80000000},
	}
	bias, err := FindELFLoadBias(file, maps, &target)
	want := uint64(0x80000000) - pageSize
	if err != nil || bias != want {
		t.Fatalf("bias = %#x, %v; want %#x", bias, err, want)
	}
	if _, err := FindELFLoadBias(file, maps[:3], &target); err == nil {
		t.Fatal("accepted an unrelated mapping with a colliding inode")
	}
	file.Progs[0].Off = pageSize
	if _, err := FindELFLoadBias(file, maps, &target); err == nil {
		t.Fatal("accepted a mapping outside the load segment")
	}
	file.Progs[0].Off = 0
	file.Progs[0].Vaddr = 0x90000000
	if _, err := FindELFLoadBias(file, maps, &target); err == nil {
		t.Fatal("accepted an underflowing relocation")
	}
}

func TestFindELFLoadBiasSplitMapping(t *testing.T) {
	pageSize := uint64(os.Getpagesize())
	file := &elf.File{Progs: []*elf.Prog{
		{ProgHeader: elf.ProgHeader{Type: elf.PT_LOAD, Off: pageSize, Vaddr: pageSize, Filesz: 6*pageSize + pageSize/2, Flags: elf.PF_R}},
		{ProgHeader: elf.ProgHeader{Type: elf.PT_LOAD, Off: pageSize, Vaddr: 0x400000 + pageSize, Filesz: 6*pageSize + pageSize/2, Flags: elf.PF_R | elf.PF_X}},
	}}
	for _, tc := range []struct {
		name    string
		offset  uint64
		start   uint64
		wantErr bool
	}{
		{"segment start", pageSize, 0x80000000, false},
		{"split segment", 3 * pageSize, 0x80000000 + 2*pageSize, false},
		{"last partial page", 7 * pageSize, 0x80000000 + 6*pageSize, false},
		{"before segment", 0, 0x80000000 - pageSize, true},
		{"after segment", 8 * pageSize, 0x80000000 + 7*pageSize, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mapping := ProcMap{Inode: 42, Start: tc.start, Offset: tc.offset, Perms: "r-xp"}
			bias, err := FindELFLoadBias(file, []ProcMap{mapping}, &mapping)
			if tc.wantErr {
				if err == nil {
					t.Fatal("accepted a mapping outside the segment")
				}
				return
			}
			want := uint64(0x80000000) - (0x400000 + pageSize)
			if err != nil || bias != want {
				t.Fatalf("bias=%#x, error=%v; want %#x", bias, err, want)
			}
		})
	}
}
