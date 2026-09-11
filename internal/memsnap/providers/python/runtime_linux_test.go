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

	"github.com/ccfos/huatuo/internal/memsnap"
)

func TestInspectModuleSymbolOverflow(t *testing.T) {
	const bias = uint64(0x10000)
	const names = "\x00_PyRuntime\x00Py_Version\x00"
	for _, tc := range []struct {
		name    string
		runtime uint64
		version uint64
		wantErr string
	}{
		{"ordinary", 0x1000, 0x2000, ""},
		{"runtime overflow", ^uint64(0) - bias + 0x1001, 0x2000, "_PyRuntime"},
		{"version overflow", 0x1000, ^uint64(0) - bias + 0x2001, "Py_Version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A minimal shared ELF keeps the test independent of an installed Python.
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
				elf.Section64{Type: uint32(elf.SHT_STRTAB), Off: 384, Size: uint64(len(names))},
				elf.Section64{Type: uint32(elf.SHT_DYNSYM), Off: 312, Size: 72, Link: 1, Entsize: 24},
				elf.Sym64{},
				elf.Sym64{Name: 1, Shndx: 1, Value: tc.runtime},
				elf.Sym64{Name: 12, Shndx: 1, Value: tc.version},
			} {
				if err := binary.Write(&data, binary.LittleEndian, value); err != nil {
					t.Fatal(err)
				}
			}
			data.WriteString(names)
			path := filepath.Join(t.TempDir(), "libpython3.12.so")
			if err := os.WriteFile(path, data.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			raw := sparseMemory{}
			// Valid bytes at a wrapped address must not hide relocation overflow.
			raw.put32(bias+tc.version, 3<<24|12<<16)
			memory := &countingMemory{memoryReader: raw, reads: make(map[uint64]int)}
			target, err := inspectModule(context.Background(), path,
				[]memsnap.ProcMap{{Start: bias, End: bias + 0x10000, Inode: 1}}, memory)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr+" address overflows") {
					t.Fatalf("error=%v, want %s overflow rejection", err, tc.wantErr)
				}
				if len(memory.reads) != 0 {
					t.Fatalf("read target memory before rejecting overflow: %v", memory.reads)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if target.runtimeAddress != bias+tc.runtime || target.version.minor != 12 {
				t.Fatalf("unexpected relocated runtime: %+v", target)
			}
		})
	}
}

func TestDiscoverRuntimeSymbolErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		offset uint64
		status memsnap.Status
		reason string
	}{
		{"read failure", 4096, memsnap.StatusFailed, "EOF"},
		{"no runtime symbol", 256, memsnap.StatusUnavailable, "no mapped module exports _PyRuntime"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Only the symbol table varies, so failures must survive runtime discovery.
			header := elf.Header64{
				Type: uint16(elf.ET_EXEC), Machine: uint16(elf.EM_X86_64),
				Version: 1, Ehsize: 64, Shoff: 64, Shentsize: 64, Shnum: 3,
			}
			copy(header.Ident[:], "\x7fELF\x02\x01\x01")
			var data bytes.Buffer
			for _, value := range []any{
				header,
				elf.Section64{},
				elf.Section64{Type: uint32(elf.SHT_STRTAB), Off: 280, Size: 1},
				elf.Section64{Type: uint32(elf.SHT_DYNSYM), Off: tc.offset, Size: 24, Link: 1, Entsize: 24},
				elf.Sym64{},
				uint8(0),
			} {
				if err := binary.Write(&data, binary.LittleEndian, value); err != nil {
					t.Fatal(err)
				}
			}
			procRoot := t.TempDir()
			pidRoot := filepath.Join(procRoot, "42")
			if err := os.Mkdir(pidRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(pidRoot, "exe"), data.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(pidRoot, "maps"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := discoverRuntime(context.Background(), procRoot, 42, nil)
			result := captureResult(nil, err)
			if result.Status != tc.status || !strings.Contains(result.Reason, tc.reason) {
				t.Fatalf("status=%q reason=%q, want %q containing %q",
					result.Status, result.Reason, tc.status, tc.reason)
			}
		})
	}
}

type sparseMemory map[uint64]byte

type countingMemory struct {
	memoryReader
	reads map[uint64]int
}

func (m *countingMemory) read(address uint64, size int) ([]byte, error) {
	m.reads[address]++
	return m.memoryReader.read(address, size)
}

func (m sparseMemory) read(address uint64, size int) ([]byte, error) {
	result := make([]byte, size)
	_ = m.readInto(address, result)
	return result, nil
}

func (m sparseMemory) readInto(address uint64, destination []byte) error {
	for offset := range destination {
		destination[offset] = m[address+uint64(offset)]
	}
	return nil
}

func (m sparseMemory) put(address uint64, data []byte) {
	for offset, value := range data {
		m[address+uint64(offset)] = value
	}
}

func (m sparseMemory) put32(address uint64, value uint32) {
	raw := make([]byte, 4)
	binary.LittleEndian.PutUint32(raw, value)
	m.put(address, raw)
}
