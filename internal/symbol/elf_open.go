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
	"fmt"
	"io"
	"os"
)

// Header objects and their backing slices cost more than their wire encoding.
const elfHeaderMemoryAllowance = 512

type boundedELF struct {
	*elf.File
	input *os.File
}

func (f *boundedELF) Close() error { return f.input.Close() }

func openBoundedELF(path string, limit uint64) (*boundedELF, error) {
	input, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := input.Stat()
	if err == nil {
		err = preflightELF(input, uint64(info.Size()), limit)
	}
	if err != nil {
		_ = input.Close()
		return nil, err
	}
	f, err := elf.NewFile(input)
	if err != nil {
		_ = input.Close()
		return nil, err
	}
	return &boundedELF{File: f, input: input}, nil
}

func readELFHeader(r io.ReaderAt, offset, fileSize uint64, order binary.ByteOrder, value any) error {
	size := uint64(binary.Size(value))
	if offset > fileSize || size > fileSize-offset {
		return io.ErrUnexpectedEOF
	}
	data := make([]byte, size)
	if _, err := r.ReadAt(data, int64(offset)); err != nil {
		return err
	}
	return binary.Read(bytes.NewReader(data), order, value)
}

// Use fixed-size reads before debug/elf allocates header arrays or .shstrtab.
func preflightELF(r io.ReaderAt, fileSize, limit uint64) error {
	var ident [elf.EI_NIDENT]byte
	if _, err := r.ReadAt(ident[:], 0); err != nil {
		return err
	}
	if string(ident[:4]) != elf.ELFMAG {
		return fmt.Errorf("invalid ELF magic")
	}
	var order binary.ByteOrder
	switch elf.Data(ident[elf.EI_DATA]) {
	case elf.ELFDATA2LSB:
		order = binary.LittleEndian
	case elf.ELFDATA2MSB:
		order = binary.BigEndian
	default:
		return fmt.Errorf("invalid ELF byte order")
	}
	var shoff, phoff, shnum, phnum, shstr, shsize, phsize uint64
	class := elf.Class(ident[elf.EI_CLASS])
	switch class {
	case elf.ELFCLASS32:
		var h elf.Header32
		if err := readELFHeader(r, 0, fileSize, order, &h); err != nil {
			return err
		}
		shoff, phoff = uint64(h.Shoff), uint64(h.Phoff)
		shnum, phnum, shstr = uint64(h.Shnum), uint64(h.Phnum), uint64(h.Shstrndx)
		shsize, phsize = uint64(h.Shentsize), uint64(h.Phentsize)
	case elf.ELFCLASS64:
		var h elf.Header64
		if err := readELFHeader(r, 0, fileSize, order, &h); err != nil {
			return err
		}
		shoff, phoff = h.Shoff, h.Phoff
		shnum, phnum, shstr = uint64(h.Shnum), uint64(h.Phnum), uint64(h.Shstrndx)
		shsize, phsize = uint64(h.Shentsize), uint64(h.Phentsize)
	default:
		return fmt.Errorf("invalid ELF class")
	}
	readSection := func(index uint64) (elf.Section64, error) {
		var section elf.Section64
		if shsize == 0 || shoff > fileSize || index > (fileSize-shoff)/shsize {
			return section, io.ErrUnexpectedEOF
		}
		offset := shoff + index*shsize
		if class == elf.ELFCLASS64 {
			err := readELFHeader(r, offset, fileSize, order, &section)
			return section, err
		}
		var small elf.Section32
		err := readELFHeader(r, offset, fileSize, order, &small)
		return elf.Section64{Off: uint64(small.Off), Size: uint64(small.Size), Flags: uint64(small.Flags), Link: small.Link, Info: small.Info}, err
	}
	if shoff != 0 && (shnum == 0 || shstr == uint64(elf.SHN_XINDEX) || phnum == 0xffff) {
		section, err := readSection(0)
		if err != nil {
			return err
		}
		if shnum == 0 {
			shnum = section.Size
		}
		if shstr == uint64(elf.SHN_XINDEX) {
			shstr = uint64(section.Link)
		}
		if phnum == 0xffff {
			phnum = uint64(section.Info)
		}
	}
	remaining := limit
	for _, table := range [][3]uint64{{shoff, shnum, shsize}, {phoff, phnum, phsize}} {
		offset, count, size := table[0], table[1], table[2]
		entryCost := max(size, elfHeaderMemoryAllowance)
		if count > remaining/entryCost {
			return fmt.Errorf("%w: ELF header count exceeds metadata budget", errELFSymbolLimit)
		}
		remaining -= count * entryCost
		if count != 0 && (size == 0 || offset > fileSize || count > (fileSize-offset)/size) {
			return io.ErrUnexpectedEOF
		}
	}
	if shstr == 0 {
		return nil
	}
	if shstr >= shnum {
		return fmt.Errorf("invalid ELF section-name table index")
	}
	section, err := readSection(shstr)
	if err != nil {
		return err
	}
	if section.Flags&uint64(elf.SHF_COMPRESSED) != 0 {
		if class == elf.ELFCLASS64 {
			var h elf.Chdr64
			if err := readELFHeader(r, section.Off, fileSize, order, &h); err != nil {
				return err
			}
			section.Size = h.Size
		} else {
			var h elf.Chdr32
			if err := readELFHeader(r, section.Off, fileSize, order, &h); err != nil {
				return err
			}
			section.Size = uint64(h.Size)
		}
	}
	if section.Size > remaining {
		return fmt.Errorf("%w: expanded ELF section names exceed metadata budget", errELFSymbolLimit)
	}
	return nil
}
