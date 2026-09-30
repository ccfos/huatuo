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

package symbol

import (
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ccfos/huatuo/internal/procfs"
)

func TestLibraryVirtualAddress(t *testing.T) {
	cache := &libCache{segments: []elf.ProgHeader{
		{Off: 0, Vaddr: 0x200000, Filesz: 0x1000},
		{Off: 0x1000, Vaddr: 0x401000, Filesz: 0x2000},
	}}
	for _, start := range []uintptr{0x70001000, 0x90001000} {
		mapping := &procfs.ProcMap{StartAddr: start, Offset: 0x1000}
		got, ok := cache.virtualAddress(mapping, uint64(start)+0x25)
		if !ok || got != 0x401025 {
			t.Fatalf("mapping %#x: address = %#x, %t", start, got, ok)
		}
	}
	if _, ok := cache.virtualAddress(&procfs.ProcMap{StartAddr: 0x70003000, Offset: 0x3000}, 0x70003000); ok {
		t.Fatal("accepted an offset outside the load segments")
	}
}

func TestUsymResolverNonzeroLoadSegment(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc is required to build the shared library fixture")
	}
	setTestXfsMounts(t, []string{"/"})
	tmpRoot := setupTempProcRoot(t)
	pid := uint32(1001)
	procDir := filepath.Join(tmpRoot, "proc", strconv.Itoa(int(pid)))
	root := filepath.Join(tmpRoot, "root")
	mustMkdirAll(t, procDir)
	mustMkdirAll(t, filepath.Join(root, "usr", "lib"))
	mustSymlink(t, root, filepath.Join(procDir, "root"))
	mustSymlink(t, "/usr/lib/libbiased.so", filepath.Join(procDir, "exe"))
	src := filepath.Join(tmpRoot, "biased.c")
	if err := os.WriteFile(src, []byte("int biased_function(void) { return 42; }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lib := filepath.Join(root, "usr", "lib", "libbiased.so")
	cmd := exec.Command("gcc", "-shared", "-fPIC", "-Wl,-Ttext-segment=0x200000", src, "-o", lib)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, out)
	}
	name, value, _ := firstFunctionSymbol(t, lib)
	// The mapping starts at load bias + PT_LOAD.p_vaddr, not load bias.
	mustWriteFile(t, filepath.Join(procDir, "maps"),
		"70200000-70210000 r-xp 00000000 fd:01 1001 /usr/lib/libbiased.so\n")
	frames := NewUsymResolver().UsymStackStrs(pid, []uint64{0x70000000 + value}, 1)
	if len(frames) != 1 || frames[0] != name {
		t.Fatalf("frames = %v, want %s", frames, name)
	}
}

func BenchmarkLibraryAddressTranslation(b *testing.B) {
	cache := &libCache{segments: []elf.ProgHeader{
		{Off: 0, Vaddr: 0, Filesz: 0x1000},
		{Off: 0x1000, Vaddr: 0x1000, Filesz: 0x2000},
	}}
	mapping := &procfs.ProcMap{StartAddr: 0x70001000, Offset: 0x1000}
	b.ReportAllocs()
	for b.Loop() {
		if _, ok := cache.virtualAddress(mapping, 0x70001025); !ok {
			b.Fatal("load segment not found")
		}
	}
}
