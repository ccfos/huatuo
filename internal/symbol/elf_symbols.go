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
	"context"
	"debug/elf"
	"fmt"
	"io"
	"strings"
)

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

// ReadELFVirtualRange reads a bounded, file-backed virtual address range.
func ReadELFVirtualRange(file *elf.File, address, size, maxBytes uint64) ([]byte, error) {
	if size > maxBytes {
		return nil, fmt.Errorf("ELF virtual range exceeds metadata safety limit")
	}
	for _, program := range file.Progs {
		if program.Type != elf.PT_LOAD || address < program.Vaddr || size > program.Filesz ||
			address-program.Vaddr > program.Filesz-size {
			continue
		}
		reader := program.Open()
		if _, err := reader.Seek(int64(address-program.Vaddr), io.SeekStart); err != nil {
			return nil, fmt.Errorf("seek ELF segment: %w", err)
		}
		data := make([]byte, size)
		if _, err := io.ReadFull(reader, data); err != nil {
			return nil, fmt.Errorf("read ELF segment: %w", err)
		}
		return data, nil
	}
	return nil, fmt.Errorf("virtual range %#x-%#x is not file-backed", address, address+size)
}
