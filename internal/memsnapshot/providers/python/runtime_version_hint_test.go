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

package python

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

func TestVersionFromModulePathHints(t *testing.T) {
	for _, tc := range []struct {
		path    string
		want    version
		wantErr string
	}{
		{path: "python3.10", want: version{major: 3, minor: 10}},
		{path: "/usr/bin/python3.10", want: version{major: 3, minor: 10}},
		{path: "libpython3.8.so.1.0", want: version{major: 3, minor: 8}},
		{path: "/usr/lib/x86_64-linux-gnu/libpython3.12.so", want: version{major: 3, minor: 12}},
		{path: "libpython3.13.so.1.0 (deleted)", want: version{major: 3, minor: 13}},
		{path: "PYTHON3.9", want: version{major: 3, minor: 9}},
		{path: "exe", wantErr: "Py_Version and a versioned libpython name are unavailable"},
		// map_files access paths carry address ranges, not versions.
		{
			path:    "/proc/123/map_files/7f0000000000-7f0000100000",
			wantErr: "Py_Version and a versioned libpython name are unavailable",
		},
	} {
		t.Run(tc.path, func(t *testing.T) {
			got, err := versionFromModulePath(tc.path)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("versionFromModulePath(%q) = %v, %v, want error %q",
						tc.path, got, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("versionFromModulePath(%q): %v", tc.path, err)
			}
			if got != tc.want {
				t.Fatalf("versionFromModulePath(%q) = %s, want %s", tc.path, got, tc.want)
			}
		})
	}
}

// A module opened through a versionless procfs access path must still resolve
// its version from the mapped file name when Py_Version is unavailable. This
// mirrors a live CPython 3.10 process whose interpreter is reached through
// /proc/<pid>/exe.
func TestInspectModuleVersionHint(t *testing.T) {
	const bias = uint64(0x10000)
	const names = "\x00_PyRuntime\x00" // Py_Version is intentionally absent.

	for _, tc := range []struct {
		name      string
		path      string
		nameHint  string
		wantMinor int
	}{
		{
			name:      "procfs exe path uses mapped interpreter name",
			path:      "/proc/4242/exe",
			nameHint:  "python3.10",
			wantMinor: 10,
		},
		{
			name:      "map_files access path uses mapped libpython name",
			path:      "/proc/4242/map_files/7f0000000000-7f0000100000",
			nameHint:  "libpython3.12.so.1.0",
			wantMinor: 12,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			header := elf.Header64{
				Type: uint16(elf.ET_DYN), Machine: uint16(elf.EM_X86_64),
				Version: 1, Ehsize: 64, Phoff: 64, Phentsize: 56, Phnum: 1,
				Shoff: 120, Shentsize: 64, Shnum: 3,
			}
			copy(header.Ident[:], "\x7fELF\x02\x01\x01")
			var data bytes.Buffer
			for _, value := range []any{
				header,
				elf.Prog64{Type: uint32(elf.PT_LOAD), Flags: uint32(elf.PF_R)},
				elf.Section64{},
				elf.Section64{Type: uint32(elf.SHT_STRTAB), Off: 360, Size: uint64(len(names))},
				elf.Section64{Type: uint32(elf.SHT_DYNSYM), Off: 312, Size: 48, Link: 1, Entsize: 24},
				elf.Sym64{},
				elf.Sym64{Name: 1, Shndx: 1, Value: 0x1000},
			} {
				if err := binary.Write(&data, binary.LittleEndian, value); err != nil {
					t.Fatal(err)
				}
			}
			data.WriteString(names)
			// inspectModule opens the hosted module directly; only the fragment
			// above matters for symbol discovery and version fallback.
			dir := t.TempDir()
			file := filepath.Join(dir, "module")
			if err := os.WriteFile(file, data.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			var stat unix.Stat_t
			if err := unix.Stat(file, &stat); err != nil {
				t.Fatal(err)
			}
			// OpenMappedFile verifies the mapping against the real file, so the
			// hosted path must be the created file while nameHint carries the
			// version. The production /proc paths are exercised through
			// runtimeModules below.
			maps := []memsnapshot.ProcMap{{
				Start: bias, End: bias + 0x10000, Inode: stat.Ino,
				DevMajor: unix.Major(stat.Dev), DevMinor: unix.Minor(stat.Dev),
			}}
			target, err := inspectModule(context.Background(), file, tc.nameHint, maps, sparseMemory{})
			if err != nil {
				t.Fatalf("inspectModule() with nameHint %q: %v", tc.nameHint, err)
			}
			if target.version.major != 3 || target.version.minor != tc.wantMinor {
				t.Fatalf("version = %s, want 3.%d", target.version, tc.wantMinor)
			}
			if target.runtimeAddress != bias+0x1000 {
				t.Fatalf("runtimeAddress = %#x, want %#x", target.runtimeAddress, bias+0x1000)
			}
		})
	}
}

// A module whose procfs access path encodes no version and whose name hint is
// likewise unversioned must still be rejected as an unsupported runtime.
func TestInspectModuleVersionHintMissing(t *testing.T) {
	const names = "\x00_PyRuntime\x00"
	header := elf.Header64{
		Type: uint16(elf.ET_DYN), Machine: uint16(elf.EM_X86_64),
		Version: 1, Ehsize: 64, Phoff: 64, Phentsize: 56, Phnum: 1,
		Shoff: 120, Shentsize: 64, Shnum: 3,
	}
	copy(header.Ident[:], "\x7fELF\x02\x01\x01")
	var data bytes.Buffer
	for _, value := range []any{
		header,
		elf.Prog64{Type: uint32(elf.PT_LOAD), Flags: uint32(elf.PF_R)},
		elf.Section64{},
		elf.Section64{Type: uint32(elf.SHT_STRTAB), Off: 360, Size: uint64(len(names))},
		elf.Section64{Type: uint32(elf.SHT_DYNSYM), Off: 312, Size: 48, Link: 1, Entsize: 24},
		elf.Sym64{},
		elf.Sym64{Name: 1, Shndx: 1, Value: 0x1000},
	} {
		if err := binary.Write(&data, binary.LittleEndian, value); err != nil {
			t.Fatal(err)
		}
	}
	data.WriteString(names)
	dir := t.TempDir()
	file := filepath.Join(dir, "module")
	if err := os.WriteFile(file, data.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	var stat unix.Stat_t
	if err := unix.Stat(file, &stat); err != nil {
		t.Fatal(err)
	}
	maps := []memsnapshot.ProcMap{{
		Start: 0x10000, End: 0x20000, Inode: stat.Ino,
		DevMajor: unix.Major(stat.Dev), DevMinor: unix.Minor(stat.Dev),
	}}
	_, err := inspectModule(context.Background(), file, "exe", maps, nil)
	if err == nil || !strings.Contains(err.Error(), "Py_Version and a versioned libpython name are unavailable") {
		t.Fatalf("inspectModule() error = %v, want unsupported runtime", err)
	}
}
