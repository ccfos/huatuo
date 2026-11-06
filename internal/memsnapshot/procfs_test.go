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
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/procfs"
)

func TestProcessInstanceID(t *testing.T) {
	procRoot := identityProcRootForTest(t)
	pidDir := filepath.Join(procRoot, "42")
	if err := os.Mkdir(pidDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields("S " + strings.Repeat("0 ", 49))
	fields[19] = "999"
	stat := []byte("42 (worker) " + strings.Join(fields, " "))
	if err := os.WriteFile(filepath.Join(pidDir, "stat"), stat, 0o600); err != nil {
		t.Fatal(err)
	}
	identity := ProcessInstanceID{TGID: 42, StartTimeTicks: 999}
	if err := ValidateProcessInstanceID(identity); err != nil {
		t.Fatal(err)
	}
	identity.StartTimeTicks++
	if err := ValidateProcessInstanceID(identity); err == nil {
		t.Fatal("changed process identity was accepted")
	}
}

func TestFindLoadBiasMappingIdentity(t *testing.T) {
	target := ProcMap{Inode: 42, DevMajor: 8, DevMinor: 3}
	maps := []ProcMap{
		{Inode: 42, DevMajor: 9, DevMinor: 3, Start: 0x100000},
		{Inode: 42, DevMajor: 8, DevMinor: 2, Start: 0x200000},
		{Inode: 41, DevMajor: 8, DevMinor: 3, Start: 0x300000},
		{Inode: 42, DevMajor: 8, DevMinor: 3, Start: 0x80000000},
	}
	bias, err := FindLoadBias(maps, &target, 0, 0x1000)
	if err != nil || bias != 0x7ffff000 {
		t.Fatalf("bias = %#x, %v; want %#x", bias, err, uint64(0x7ffff000))
	}
	if _, err := FindLoadBias(maps[:3], &target, 0, 0); err == nil {
		t.Fatal("accepted an unrelated mapping with a colliding inode")
	}
	if _, err := FindLoadBias(maps, &target, 0x1000, 0); err == nil {
		t.Fatal("accepted a mapping with the wrong offset")
	}
	if _, err := FindLoadBias(maps, &target, 0, 0x90000000); err == nil {
		t.Fatal("accepted an underflowing relocation")
	}
}

func TestFindELFLoadBiasSegments(t *testing.T) {
	page := uint64(os.Getpagesize())
	const base = uint64(0x70000000)
	segment := elf.ProgHeader{
		Type:   elf.PT_LOAD,
		Off:    page + 0x80,
		Vaddr:  4*page + 0x80,
		Filesz: 3 * page,
	}
	for _, test := range []struct {
		name      string
		headers   []elf.ProgHeader
		start     uint64
		offset    uint64
		want      uint64
		wantError bool
	}{
		{
			name:    "aligned segment start",
			headers: []elf.ProgHeader{segment},
			start:   base + 4*page,
			offset:  page,
			want:    base,
		},
		{
			name:    "split mapping",
			headers: []elf.ProgHeader{segment},
			start:   base + 5*page,
			offset:  2 * page,
			want:    base,
		},
		{
			name:    "last partial page",
			headers: []elf.ProgHeader{segment},
			start:   base + 7*page,
			offset:  4 * page,
			want:    base,
		},
		{
			name:    "separate load instance",
			headers: []elf.ProgHeader{segment},
			start:   base + 0x100000 + 4*page,
			offset:  page,
			want:    base + 0x100000,
		},
		{
			name:      "offset outside segment",
			headers:   []elf.ProgHeader{segment},
			start:     base,
			offset:    5 * page,
			wantError: true,
		},
		{
			name:      "underflow",
			headers:   []elf.ProgHeader{segment},
			start:     page,
			offset:    page,
			wantError: true,
		},
		{
			name:      "unaligned start",
			headers:   []elf.ProgHeader{segment},
			start:     base + 4*page + 1,
			offset:    page,
			wantError: true,
		},
		{
			name:      "unaligned offset",
			headers:   []elf.ProgHeader{segment},
			start:     base + 4*page,
			offset:    page + 1,
			wantError: true,
		},
		{
			name: "overflowing file range",
			headers: []elf.ProgHeader{{
				Type:   elf.PT_LOAD,
				Off:    ^uint64(0) - page + 1,
				Filesz: page,
			}},
			start:     base,
			wantError: true,
		},
		{
			name:      "missing load segment",
			start:     base,
			wantError: true,
		},
		{
			name: "ambiguous segments",
			headers: []elf.ProgHeader{segment, {
				Type:   elf.PT_LOAD,
				Off:    page,
				Vaddr:  8 * page,
				Filesz: page,
			}},
			start:     base + 8*page,
			offset:    page,
			wantError: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			file := &elf.File{}
			for _, header := range test.headers {
				file.Progs = append(file.Progs, &elf.Prog{ProgHeader: header})
			}
			target := ProcMap{
				Start:  test.start,
				Offset: test.offset,
				Inode:  42,
			}
			// Another load of the same inode must never supply this instance's bias.
			maps := []ProcMap{
				{
					Start:  base,
					Offset: test.offset,
					Inode:  42,
				},
				target,
			}
			bias, err := FindELFLoadBias(file, maps, &maps[1])
			if (err != nil) != test.wantError || err == nil && bias != test.want {
				t.Fatalf("bias=%#x err=%v, want %#x error=%t", bias, err, test.want, test.wantError)
			}
		})
	}
}

func TestFindELFLoadBiasSharedFilePage(t *testing.T) {
	page := uint64(os.Getpagesize())
	const base = uint64(0x70000000)
	file := &elf.File{Progs: []*elf.Prog{
		{ProgHeader: elf.ProgHeader{
			Type:   elf.PT_LOAD,
			Flags:  elf.PF_R | elf.PF_X,
			Filesz: page - 0x600,
		}},
		{ProgHeader: elf.ProgHeader{
			Type:   elf.PT_LOAD,
			Flags:  elf.PF_R | elf.PF_W,
			Off:    page - 0x230,
			Vaddr:  2*page - 0x230,
			Filesz: 0x248,
		}},
	}}
	for _, test := range []struct {
		name        string
		start       uint64
		offset      uint64
		permissions string
		wantError   bool
	}{
		{
			name:        "executable page excludes RW segment",
			start:       base,
			permissions: "r-xp",
		},
		{
			name:        "writable page excludes RX segment",
			start:       base + page,
			permissions: "rw-p",
		},
		{
			name:        "last writable partial page",
			start:       base + 2*page,
			offset:      page,
			permissions: "rw-p",
		},
		{
			name:        "RELRO retains ambiguity without loader identity",
			start:       base + page,
			permissions: "r--p",
			wantError:   true,
		},
		{
			name:      "unknown permissions do not guess",
			start:     base,
			wantError: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			mapping := ProcMap{
				Start:  test.start,
				Offset: test.offset,
				Perms:  test.permissions,
				Inode:  42,
			}
			maps := []ProcMap{mapping}
			bias, err := FindELFLoadBias(file, maps, &maps[0])
			if (err != nil) != test.wantError || err == nil && bias != base {
				t.Fatalf("bias=%#x err=%v, want %#x error=%t", bias, err, base, test.wantError)
			}
		})
	}
}

func TestReadProcessInstanceIDStat(t *testing.T) {
	// Include the complete stat record because procfs parses more than starttime.
	fields := strings.Fields("S " + strings.Repeat("0 ", 49))
	fields[19] = "987654321"
	valid := "123 (worker (heap)) " + strings.Join(fields, " ")
	fields[19] = "0"
	zero := "123 (worker (heap)) " + strings.Join(fields, " ")

	for _, test := range []struct {
		name      string
		stat      string
		want      uint64
		wantError bool
	}{
		{name: "raw ticks and parentheses in comm", stat: valid, want: 987654321},
		{name: "zero starttime", stat: zero},
		{name: "truncated stat", stat: "123 (worker) S 0", wantError: true},
		{name: "missing stat", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			procRoot := identityProcRootForTest(t)
			// A real PID ensures the fixture detects accidental use of /proc.
			pid := os.Getpid()
			directory := filepath.Join(procRoot, strconv.Itoa(pid))
			if err := os.Mkdir(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			if test.stat != "" {
				if err := os.WriteFile(filepath.Join(directory, "stat"), []byte(test.stat), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			identity, err := ReadProcessInstanceID(pid)
			got := identity.StartTimeTicks
			if (err != nil) != test.wantError || got != test.want {
				t.Fatalf("starttime = %d, error = %v; want %d, error = %t", got, err, test.want, test.wantError)
			}
			if err == nil {
				if err := ValidateProcessInstanceID(identity); err != nil {
					t.Fatalf("validate read identity: %v", err)
				}
			}
			if test.stat == "" && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("missing stat error = %v, want os.ErrNotExist", err)
			}
		})
	}
}

func TestReadProcessInstanceIDCurrentProcess(t *testing.T) {
	identity, err := ReadProcessInstanceID(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if identity.TGID != os.Getpid() {
		t.Fatalf("identity=%+v", identity)
	}
	if err := ValidateProcessInstanceID(identity); err != nil {
		t.Fatal(err)
	}
	for _, pid := range []int{0, -1} {
		if _, err := ReadProcessInstanceID(pid); err == nil {
			t.Fatalf("accepted pid %d", pid)
		}
	}
}

// These tests are serial because RootPrefix changes the process-wide mount paths.
func identityProcRootForTest(t *testing.T) string {
	t.Helper()
	previous := filepath.Dir(procfs.DefaultPath())
	root := t.TempDir()
	procRoot := filepath.Join(root, "proc")
	if err := os.Mkdir(procRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	procfs.RootPrefix(root)
	t.Cleanup(func() { procfs.RootPrefix(previous) })
	return procRoot
}
