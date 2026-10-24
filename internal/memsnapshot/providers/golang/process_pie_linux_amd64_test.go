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
	"os"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

func TestPIEExecutableDataMapping(t *testing.T) {
	executable, file, want := pieExecutable(t)
	before, err := resolveExecutableLoadBias(t.Context(), os.Getpid(), file)
	if err != nil || before != want {
		t.Fatalf("initial load bias=%#x, want=%#x, error=%v", before, want, err)
	}
	mapping, err := unix.Mmap(int(executable.Fd()), 0, os.Getpagesize(),
		unix.PROT_READ, unix.MAP_PRIVATE|unix.MAP_32BIT)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := unix.Munmap(mapping); err != nil {
			t.Error(err)
		}
	}()
	after, biasErr := resolveExecutableLoadBias(t.Context(), os.Getpid(), file)
	t.Logf("actual=%#x, before=%#x, after=%#x, data mapping=%#x, error=%v",
		want, before, after, uintptr(unsafe.Pointer(&mapping[0])), biasErr)
	if biasErr != nil || after != want {
		t.Fatalf("data mapping changed executable load bias: got=%#x want=%#x error=%v",
			after, want, biasErr)
	}
	checkPIEProcessReader(t, want)
}

func TestPIEExecutableSplitMapping(t *testing.T) {
	_, file, want := pieExecutable(t)
	maps, err := memsnapshot.ReadProcMaps("/proc/self/maps")
	if err != nil {
		t.Fatal(err)
	}
	var mapping *memsnapshot.ProcMap
	for _, program := range file.Progs {
		if program.Type != elf.PT_LOAD || program.Off != 0 || program.Flags&elf.PF_X == 0 {
			continue
		}
		for index := range maps {
			if maps[index].Start == want+program.Vaddr {
				mapping = &maps[index]
				break
			}
		}
	}
	if mapping == nil || mapping.Offset != 0 || mapping.Perms != "r-xp" {
		t.Skip("requires an executable ELF header page")
	}
	pageSize := uintptr(os.Getpagesize())
	text := file.Section(".text")
	if text == nil || want+text.Addr < mapping.Start+uint64(pageSize) {
		t.Skip("ELF headers share a page with code")
	}
	// Restrict only the ELF header page, not any executing Go instructions.
	if _, _, err := unix.Syscall(unix.SYS_MPROTECT, uintptr(mapping.Start), pageSize, unix.PROT_READ); err != 0 {
		t.Fatal(err)
	}
	defer func() {
		if _, _, err := unix.Syscall(unix.SYS_MPROTECT, uintptr(mapping.Start), pageSize, unix.PROT_READ|unix.PROT_EXEC); err != 0 {
			t.Error(err)
		}
	}()
	bias, err := resolveExecutableLoadBias(t.Context(), os.Getpid(), file)
	if err != nil || bias != want {
		t.Fatalf("split executable load bias=%#x, want=%#x, error=%v", bias, want, err)
	}
	checkPIEProcessReader(t, want)
}

func pieExecutable(t *testing.T) (*os.File, *elf.File, uint64) {
	t.Helper()
	executable, err := os.Open("/proc/self/exe")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = executable.Close() })
	file, err := elf.NewFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	if file.Type != elf.ET_DYN {
		t.Skip("run with go test -buildmode=pie")
	}
	symbols, err := lookupRuntimeSymbols(t.Context(), file)
	if err != nil {
		t.Fatalf("runtime symbols: %+v, %v", symbols, err)
	}
	if !symbols.hasMemProfileRate {
		t.Skip("run with -ldflags='-s=false -w=false'")
	}
	want := uint64(uintptr(unsafe.Pointer(&runtime.MemProfileRate))) - symbols.memProfileRate
	return executable, file, want
}

func checkPIEProcessReader(t *testing.T, want uint64) {
	t.Helper()
	identity, err := memsnapshot.ReadProcessInstanceID(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	reader, readErr := newProcessReader(t.Context(), identity)
	if reader != nil {
		defer reader.Close()
	}
	t.Logf("production reader error=%v", readErr)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if reader.runtime.loadBias != want || reader.runtime.memProfileRate != int64(runtime.MemProfileRate) {
		t.Fatalf("runtime load bias=%#x, profile rate=%d; want %#x, %d",
			reader.runtime.loadBias, reader.runtime.memProfileRate, want, runtime.MemProfileRate)
	}
}
