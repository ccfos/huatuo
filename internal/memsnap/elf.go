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

package memsnap

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	maxDetectionELFHeaders = 4096
	maxDetectionELFBytes   = 8 << 20
)

// ReadELFMetadata bounds the headers and section names read by debug/elf.NewFile.
// Symbol tables are not read for language detection and must not consume its
// budget. Dependency metadata is bounded separately when elfLinksPython reads it.
func ReadELFMetadata(ctx context.Context, reader io.ReaderAt) (*elf.File, error) {
	r := contextReaderAt{ctx: ctx, reader: reader}
	var header [64]byte
	if _, err := r.ReadAt(header[:], 0); err != nil {
		return nil, err
	}
	if string(header[:4]) != "\x7fELF" {
		return nil, fmt.Errorf("executable is not ELF")
	}
	var order binary.ByteOrder
	switch elf.Data(header[5]) {
	case elf.ELFDATA2LSB:
		order = binary.LittleEndian
	case elf.ELFDATA2MSB:
		order = binary.BigEndian
	default:
		return nil, fmt.Errorf("invalid ELF byte order")
	}
	var shoff uint64
	var shentsize, shnum, phnum, phentsize, shstrndx uint16
	switch elf.Class(header[4]) {
	case elf.ELFCLASS64:
		shoff = order.Uint64(header[40:48])
		phentsize = order.Uint16(header[54:56])
		shstrndx = order.Uint16(header[62:64])
		if phentsize != 0 && phentsize != 56 {
			return nil, fmt.Errorf("ELF program headers exceed detection budget")
		}
		shentsize, shnum, phnum = order.Uint16(header[58:60]), order.Uint16(header[60:62]), order.Uint16(header[56:58])
		if shentsize != 64 && shoff != 0 {
			return nil, fmt.Errorf("invalid ELF section header size")
		}
	case elf.ELFCLASS32:
		shoff = uint64(order.Uint32(header[32:36]))
		phentsize = order.Uint16(header[42:44])
		shstrndx = order.Uint16(header[50:52])
		if phentsize != 0 && phentsize != 32 {
			return nil, fmt.Errorf("ELF program headers exceed detection budget")
		}
		shentsize, shnum, phnum = order.Uint16(header[46:48]), order.Uint16(header[48:50]), order.Uint16(header[44:46])
		if shentsize != 40 && shoff != 0 {
			return nil, fmt.Errorf("invalid ELF section header size")
		}
	default:
		return nil, fmt.Errorf("invalid ELF class")
	}
	if (shnum != 0 && shoff == 0) || (phnum != 0 && phentsize == 0) || shnum > maxDetectionELFHeaders || phnum > maxDetectionELFHeaders || (shoff != 0 && shnum == 0) {
		return nil, fmt.Errorf("ELF header count exceeds detection budget")
	}
	// Keep allocation-driving headers immutable across validation and parsing.
	sections := make([]byte, int(shnum)*int(shentsize))
	var nameOffset, nameSize uint64
	for i := uint16(0); i < shnum; i++ {
		offset := shoff + uint64(i)*uint64(shentsize)
		if offset < shoff || offset > (1<<63-1)-64 {
			return nil, fmt.Errorf("ELF section offset overflows")
		}
		var section [64]byte
		if _, err := r.ReadAt(section[:shentsize], int64(offset)); err != nil {
			return nil, err
		}
		copy(sections[int(i)*int(shentsize):], section[:shentsize])
		typ := elf.SectionType(order.Uint32(section[4:8]))
		var size, flags uint64
		if elf.Class(header[4]) == elf.ELFCLASS64 {
			flags, size = order.Uint64(section[8:16]), order.Uint64(section[32:40])
		} else {
			flags, size = uint64(order.Uint32(section[8:12])), uint64(order.Uint32(section[20:24]))
		}
		if i == shstrndx && shstrndx != 0 {
			if typ != elf.SHT_STRTAB || flags&uint64(elf.SHF_COMPRESSED) != 0 || size > 1<<20 {
				return nil, fmt.Errorf("ELF section names exceed detection budget")
			}
			nameSize = size
			if elf.Class(header[4]) == elf.ELFCLASS64 {
				nameOffset = order.Uint64(section[24:32])
			} else {
				nameOffset = uint64(order.Uint32(section[16:20]))
			}
		}
	}
	if shstrndx != 0 && (shstrndx >= shnum || nameOffset > 1<<63-1) {
		return nil, fmt.Errorf("invalid ELF section names index/offset")
	}
	names := make([]byte, int(nameSize))
	if len(names) > 0 {
		if _, err := r.ReadAt(names, int64(nameOffset)); err != nil {
			return nil, err
		}
	}
	if shstrndx != 0 {
		for i := uint16(0); i < shnum; i++ {
			offset := order.Uint32(sections[int(i)*int(shentsize):])
			if uint64(offset) >= uint64(len(names)) {
				return nil, fmt.Errorf("invalid ELF section name offset")
			}
			name := names[offset:]
			if len(name) > 256 {
				name = name[:256]
			}
			if bytes.IndexByte(name, 0) < 0 {
				return nil, fmt.Errorf("ELF section name exceeds detection budget")
			}
		}
	}
	file, err := elf.NewFile(&frozenELFHeaders{
		reader: r, header: header[:],
		sections: sections, sectionOffset: int64(shoff), names: names, nameOffset: int64(nameOffset),
	})
	if err != nil {
		return nil, err
	}
	return file, nil
}

type frozenELFHeaders struct {
	names            []byte
	nameOffset       int64
	reader           io.ReaderAt
	header, sections []byte
	sectionOffset    int64
}

func (r *frozenELFHeaders) ReadAt(p []byte, offset int64) (int, error) {
	total := 0
	for len(p) > 0 {
		var cached []byte
		switch {
		case offset >= 0 && offset < int64(len(r.header)):
			cached = r.header[offset:]
		case offset >= r.sectionOffset && offset-r.sectionOffset < int64(len(r.sections)):
			cached = r.sections[offset-r.sectionOffset:]
		case offset >= r.nameOffset && offset-r.nameOffset < int64(len(r.names)):
			cached = r.names[offset-r.nameOffset:]
		default:
			length := len(p)
			if offset >= 0 && offset < r.sectionOffset && int64(length) > r.sectionOffset-offset {
				length = int(r.sectionOffset - offset)
			}
			if offset >= 0 && offset < r.nameOffset && int64(length) > r.nameOffset-offset {
				length = int(r.nameOffset - offset)
			}
			n, err := r.reader.ReadAt(p[:length], offset)
			total += n
			if err != nil {
				return total, err
			}
			p, offset = p[n:], offset+int64(n)
			continue
		}
		n := copy(p, cached)
		p, offset, total = p[n:], offset+int64(n), total+n
	}
	return total, nil
}

// ReadELFSymbols reads only the requested symbols. It deliberately does not
// decode GNU symbol versions: runtime address lookup does not use them.
// Budgets cover raw tables, retained symbols, and repeated name scanning.
func ReadELFSymbols(ctx context.Context, file *elf.File, typ elf.SectionType,
	maxBytes, maxSymbols uint64, wanted func(string) bool,
) ([]elf.Symbol, error) {
	section := file.SectionByType(typ)
	if section == nil {
		return nil, elf.ErrNoSymbols
	}
	entrySize := uint64(elf.Sym64Size)
	if file.Class == elf.ELFCLASS32 {
		entrySize = elf.Sym32Size
	} else if file.Class != elf.ELFCLASS64 {
		return nil, fmt.Errorf("unsupported ELF symbol class")
	}
	if section.Size%entrySize != 0 || section.Size/entrySize > maxSymbols ||
		section.Entsize != entrySize || int(section.Link) >= len(file.Sections) {
		return nil, fmt.Errorf("invalid or oversized ELF symbol table")
	}
	names := file.Sections[section.Link]
	if names.Type != elf.SHT_STRTAB {
		return nil, fmt.Errorf("ELF symbols do not link to a string table")
	}
	remaining := maxBytes
	for _, table := range []*elf.Section{section, names} {
		if table.Flags&elf.SHF_COMPRESSED != 0 ||
			strings.HasPrefix(table.Name, ".zdebug") || table.Size > remaining {
			return nil, fmt.Errorf("ELF symbol tables exceed metadata budget or are compressed")
		}
		remaining -= table.Size
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := section.Data()
	if err != nil {
		return nil, err
	}
	stringsData, err := names.Data()
	if err != nil {
		return nil, err
	}
	var result []elf.Symbol
	scanRemaining := maxBytes
	for offset := entrySize; offset < uint64(len(data)); offset += entrySize {
		if offset/entrySize%128 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		raw := data[offset : offset+entrySize]
		nameOffset := uint64(file.ByteOrder.Uint32(raw[:4]))
		if nameOffset >= uint64(len(stringsData)) {
			return nil, fmt.Errorf("invalid ELF symbol name offset")
		}
		window := stringsData[nameOffset:]
		if len(window) > 4096 {
			window = window[:4096]
		}
		length := bytes.IndexByte(window, 0)
		if length < 0 || uint64(length+1) > scanRemaining {
			return nil, fmt.Errorf("ELF symbol names exceed metadata budget")
		}
		scanRemaining -= uint64(length + 1)
		name := string(window[:length])
		if !wanted(name) {
			continue
		}
		cost := uint64(length + 128)
		if len(result) >= 4096 || cost > remaining {
			return nil, fmt.Errorf("retained ELF symbols exceed metadata budget")
		}
		remaining -= cost
		symbol := elf.Symbol{Name: name}
		if file.Class == elf.ELFCLASS64 {
			symbol.Info, symbol.Other = raw[4], raw[5]
			symbol.Section = elf.SectionIndex(file.ByteOrder.Uint16(raw[6:8]))
			symbol.Value, symbol.Size = file.ByteOrder.Uint64(raw[8:16]), file.ByteOrder.Uint64(raw[16:24])
		} else {
			symbol.Value, symbol.Size = uint64(file.ByteOrder.Uint32(raw[4:8])), uint64(file.ByteOrder.Uint32(raw[8:12]))
			symbol.Info, symbol.Other = raw[12], raw[13]
			symbol.Section = elf.SectionIndex(file.ByteOrder.Uint16(raw[14:16]))
		}
		result = append(result, symbol)
	}
	return result, ctx.Err()
}

// FindELFLoadBias tries load segments in ELF order because the first segment
// may not have a matching process mapping.
func FindELFLoadBias(file *elf.File, mappings []ProcMap, inode uint64) (uint64, error) {
	pageSize := uint64(os.Getpagesize())
	for _, program := range file.Progs {
		if program.Type != elf.PT_LOAD {
			continue
		}
		loadOffset := program.Off &^ (pageSize - 1)
		loadAddress := program.Vaddr &^ (pageSize - 1)
		if bias, err := FindLoadBias(mappings, inode, loadOffset, loadAddress); err == nil {
			return bias, nil
		}
	}
	return 0, errors.New("ELF load bias not found")
}
