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
	"bytes"
	"debug/elf"
	"encoding/binary"
	"errors"
	"os"
	"testing"
)

func TestResolverBoundsELFOpening(t *testing.T) {
	const budget = 4096
	for _, kind := range []string{"raw-names", "compressed-names", "extended-sections", "extended-programs"} {
		t.Run(kind, func(t *testing.T) {
			file := "symbols64.elf"
			if kind == "compressed-names" {
				file = "compressed64.elf"
			}
			image := readELFFixture(t, file)
			var header elf.Header64
			if err := binary.Read(bytes.NewReader(image), binary.LittleEndian, &header); err != nil {
				t.Fatal(err)
			}
			header.Shstrndx = 1
			switch kind {
			case "raw-names":
				names := elf.Section64{Type: uint32(elf.SHT_STRTAB), Off: uint64(len(image)), Size: budget}
				encoded, _ := binary.Append(nil, binary.LittleEndian, names)
				copy(image[header.Shoff+uint64(header.Shentsize):], encoded)
				image = append(image, make([]byte, budget)...)
			case "extended-sections":
				header.Shnum = 0
				encoded, _ := binary.Append(nil, binary.LittleEndian, elf.Section64{Size: uint64(elf.SHN_LORESERVE)})
				copy(image[header.Shoff:], encoded)
			case "extended-programs":
				header.Phnum = 0xffff
				header.Phentsize = uint16(binary.Size(elf.Prog64{}))
				// A small Info must not hide debug/elf's raw program count.
				encoded, _ := binary.Append(nil, binary.LittleEndian, elf.Section64{Info: 1})
				copy(image[header.Shoff:], encoded)
			}
			encoded, _ := binary.Append(nil, binary.LittleEndian, header)
			copy(image, encoded)
			for _, entry := range []string{"executable", "library", "resolve"} {
				t.Run(entry, func(t *testing.T) {
					resolver, pid, _, _ := setupMainElfResolverFixture(t)
					path, err := resolver.exePath(pid)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, image, 0o600); err != nil {
						t.Fatal(err)
					}
					resolver.elfSymbolLimits.MaxMetadataBytes = budget
					switch entry {
					case "executable":
						_, err = resolver.loadElfCaches(pid)
					case "library":
						_, err = resolver.loadLibCache(pid, path)
					case "resolve":
						_, err = resolver.resolveELFPCs(path, &elfSymbolCache{}, []uint64{0x1000})
					}
					if !errors.Is(err, errELFSymbolLimit) {
						t.Fatalf("got %v, want ELF limit error", err)
					}
					if len(resolver.exeCache) != 0 || len(resolver.libCaches) != 0 {
						t.Fatal("rejected ELF entered cache")
					}
				})
			}
		})
	}
}

func TestPreflightELFNameBudget(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		file := "symbols64.elf"
		if compressed {
			file = "compressed64.elf"
		}
		image := readELFFixture(t, file)
		namesSize := openELFFixture(t, image).Sections[1].Size
		var header elf.Header64
		if err := binary.Read(bytes.NewReader(image), binary.LittleEndian, &header); err != nil {
			t.Fatal(err)
		}
		header.Shstrndx = 1
		encoded, _ := binary.Append(nil, binary.LittleEndian, header)
		copy(image, encoded)
		budget := uint64(header.Shnum)*(uint64(header.Shentsize)+elfHeaderMemoryAllowance) +
			uint64(header.Shnum+1)*namesSize
		if err := preflightELF(bytes.NewReader(image), uint64(len(image)), budget); err != nil {
			t.Fatalf("compressed=%v: exact budget: %v", compressed, err)
		}
		if err := preflightELF(bytes.NewReader(image), uint64(len(image)), budget-1); !errors.Is(err, errELFSymbolLimit) {
			t.Fatalf("compressed=%v: below budget: %v", compressed, err)
		}
	}
}

func TestPreflightELF32ByteOrders(t *testing.T) {
	for _, data := range []elf.Data{elf.ELFDATA2LSB, elf.ELFDATA2MSB} {
		var order binary.ByteOrder = binary.LittleEndian
		if data == elf.ELFDATA2MSB {
			order = binary.BigEndian
		}
		header := elf.Header32{
			Shnum: 1, Shentsize: uint16(binary.Size(elf.Section32{})),
			Shoff: uint32(binary.Size(elf.Header32{})),
		}
		copy(header.Ident[:], elf.ELFMAG)
		header.Ident[elf.EI_CLASS] = byte(elf.ELFCLASS32)
		header.Ident[elf.EI_DATA] = byte(data)
		var image bytes.Buffer
		for _, value := range []any{header, elf.Section32{}} {
			if err := binary.Write(&image, order, value); err != nil {
				t.Fatal(err)
			}
		}
		budget := uint64(header.Shentsize) + elfHeaderMemoryAllowance
		if err := preflightELF(bytes.NewReader(image.Bytes()), uint64(image.Len()), budget); err != nil {
			t.Fatalf("%v: %v", data, err)
		}
		if err := preflightELF(bytes.NewReader(image.Bytes()), uint64(image.Len()), budget-1); !errors.Is(err, errELFSymbolLimit) {
			t.Fatalf("%v: below budget: %v", data, err)
		}
	}
}
