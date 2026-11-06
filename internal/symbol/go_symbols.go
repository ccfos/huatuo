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
	"debug/gosym"
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	maxGoELFMetadataBytes = 64 << 20
	maxGoELFSymbols       = 1 << 20
)

// ReadGoTable reads and validates Go function metadata without taking ownership of file.
// Returned tables contain link-time PCs; callers own relocation and return-PC handling.
func ReadGoTable(ctx context.Context, file *elf.File) (*gosym.Table, error) {
	table, _, err := readGoTable(ctx, file, ELFSymbolLimits{
		MaxMetadataBytes:       maxGoSymbolBytes,
		MaxSymbolAndCacheCount: maxGoSymbolEntries,
		MaxNameBytes:           maxGoSymbolNames,
		MaxNameLength:          maxGoSymbolName,
	})
	return table, err
}

type goSymbolUsage struct {
	metadataBytes uint64
	nameBytes     uint64
	symbolCount   uint64
}

func readGoTable(ctx context.Context, file *elf.File, limits ELFSymbolLimits) (*gosym.Table, goSymbolUsage, error) {
	var usage goSymbolUsage
	if err := ctx.Err(); err != nil {
		return nil, usage, err
	}
	pcln, pclnAddr, scannedBytes, err := readPCLN(ctx, file, limits.MaxMetadataBytes)
	if err != nil {
		return nil, usage, err
	}
	limits.MaxMetadataBytes -= scannedBytes
	if err := validatePCLN(ctx, pcln, file.ByteOrder, limits, &usage); err != nil {
		return nil, usage, err
	}
	textStart, textBytes, err := goTextStart(ctx, file, pcln, pclnAddr, limits.MaxMetadataBytes-usage.metadataBytes)
	if err != nil {
		return nil, usage, err
	}
	usage.metadataBytes += scannedBytes + textBytes
	if err := ctx.Err(); err != nil {
		return nil, usage, err
	}
	// Supported Go releases (1.18+) keep symbols in pclntab. Do not parse
	// obsolete .gosymtab records, whose names can expand during decoding.
	table, err := gosym.NewTable(nil, gosym.NewLineTable(pcln, textStart))
	if err != nil {
		return nil, usage, fmt.Errorf("parse Go symbol table: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, usage, err
	}
	return table, usage, nil
}

const (
	maxGoSymbolEntries = 1 << 18
	maxGoSymbolNames   = 16 << 20
	maxGoSymbolName    = 4 << 10
	maxGoSymbolBytes   = 64 << 20
	// Include Func/Sym storage, name/file maps (including growth), and
	// fixed parser state. These are conservative charges, not an RSS cap.
	goSymbolFunctionBytes = 256
	goSymbolFileBytes     = 256
	goSymbolFixedBytes    = 4 << 10
)

func goSymbolBudget(rawBytes, functions, files uint64) (uint64, error) {
	remaining := uint64(maxGoSymbolBytes - goSymbolFixedBytes)
	for _, charge := range []struct{ count, size uint64 }{
		{rawBytes, 1}, {functions, goSymbolFunctionBytes}, {files, goSymbolFileBytes},
	} {
		if charge.count > remaining/charge.size {
			return 0, fmt.Errorf("Go symbol metadata exceeds total memory budget")
		}
		remaining -= charge.count * charge.size
	}
	return remaining, nil
}

// File-byte limits alone do not bound gosym's count-driven allocations or
// overlapping names. Validate the supported layout before invoking the parser.
func validatePCLN(ctx context.Context, data []byte, order binary.ByteOrder, limits ELFSymbolLimits, usage *goSymbolUsage) error {
	if _, err := pclnTextStart(data, order); err != nil {
		return err
	}
	ptrSize := int(data[7])
	headerSize := 8 + 8*ptrSize
	if len(data) < headerSize || data[4] != 0 || data[5] != 0 ||
		(data[6] != 1 && data[6] != 2 && data[6] != 4) {
		return fmt.Errorf("invalid Go pclntab header")
	}
	word := func(index int) uint64 {
		offset := 8 + index*ptrSize
		if ptrSize == 4 {
			return uint64(order.Uint32(data[offset:]))
		}
		return order.Uint64(data[offset:])
	}
	nfunc, nfile := word(0), word(1)
	if nfunc == 0 || nfunc > maxGoSymbolEntries || nfile > maxGoSymbolEntries {
		return fmt.Errorf("Go pclntab function/file count exceeds safety limit")
	}
	budget, err := goSymbolBudget(uint64(len(data)), nfunc, nfile)
	if err != nil {
		return err
	}
	metadataBytes := uint64(maxGoSymbolBytes) - budget
	if metadataBytes > limits.MaxMetadataBytes || nfunc > limits.MaxSymbolAndCacheCount {
		return fmt.Errorf("%w: Go function metadata exceeds configured budget", errELFSymbolLimit)
	}
	budget = min(budget, limits.MaxMetadataBytes-metadataBytes)
	initialBudget := budget
	previous := uint64(headerSize)
	for index := 3; index <= 7; index++ {
		offset := word(index)
		if offset < previous || offset > uint64(len(data)) {
			return fmt.Errorf("Go pclntab table offset is out of bounds")
		}
		previous = offset
	}
	functions := data[word(7):]
	if len(functions) < 16 || nfunc*8+4 > uint64(len(functions)) {
		return fmt.Errorf("Go pclntab function table is truncated")
	}
	names := data[word(3):word(4)]
	remaining := min(uint64(maxGoSymbolNames), limits.MaxNameBytes)
	initialNames := remaining
	checkName := func(table []byte, offset uint64) (int, error) {
		if offset >= uint64(len(table)) {
			return 0, fmt.Errorf("Go pclntab name offset is out of bounds")
		}
		tail := table[offset:]
		// Zero disables the configurable per-name limit, not the Go safety cap.
		maxLength := uint64(maxGoSymbolName)
		if limits.MaxNameLength != 0 {
			maxLength = min(maxLength, limits.MaxNameLength)
		}
		if uint64(len(tail)) > maxLength+1 {
			tail = tail[:maxLength+1]
		}
		size := bytes.IndexByte(tail, 0)
		if size < 0 || uint64(size+1) > remaining {
			return 0, fmt.Errorf("%w: Go pclntab names exceed configured budget", errELFSymbolLimit)
		}
		remaining -= uint64(size + 1)
		// Charge each decoded name, even when offsets overlap, with room
		// for allocator rounding of short strings.
		charge := uint64(size + 16)
		if charge > budget {
			return 0, fmt.Errorf("%w: Go symbol metadata exceeds total memory budget", errELFSymbolLimit)
		}
		budget -= charge
		return size + 1, nil
	}
	for index := uint64(0); index < nfunc; index++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		offset := uint64(order.Uint32(functions[index*8+4:]))
		if offset < nfunc*8+4 || offset > uint64(len(functions))-16 {
			return fmt.Errorf("Go pclntab function record is out of bounds")
		}
		if _, err := checkName(names, uint64(order.Uint32(functions[offset+4:]))); err != nil {
			return err
		}
	}
	files := data[word(5):word(6)]
	position := uint64(0)
	for index := uint64(0); index < nfile; index++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		size, err := checkName(files, position)
		if err != nil {
			return err
		}
		position += uint64(size)
	}
	*usage = goSymbolUsage{
		metadataBytes: metadataBytes + initialBudget - budget,
		nameBytes:     initialNames - remaining,
		symbolCount:   nfunc,
	}
	return nil
}

func pclnTextStart(pcln []byte, byteOrder binary.ByteOrder) (uint64, error) {
	if len(pcln) < 8 {
		return 0, fmt.Errorf("Go pclntab header is truncated")
	}
	switch magic := byteOrder.Uint32(pcln[:4]); magic {
	case 0xfffffff0, 0xfffffff1:
		// Go 1.18-1.19 emit 0xfffffff0 and Go 1.20+ emit 0xfffffff1; the
		// pcHeader fields used below are identical for both.
	default:
		return 0, fmt.Errorf("unsupported Go pclntab magic %#x", magic)
	}
	pointerSize := int(pcln[7])
	if pointerSize != 4 && pointerSize != 8 {
		return 0, fmt.Errorf("unsupported Go pclntab pointer size %d", pointerSize)
	}
	// pcHeader contains nfunc and nfiles before textStart.
	offset := 8 + 2*pointerSize
	if len(pcln) < offset+pointerSize {
		return 0, fmt.Errorf("Go pclntab pcHeader is truncated")
	}
	if pointerSize == 4 {
		return uint64(byteOrder.Uint32(pcln[offset : offset+pointerSize])), nil
	}
	return byteOrder.Uint64(pcln[offset : offset+pointerSize]), nil
}

func readPCLN(ctx context.Context, file *elf.File, maxBytes uint64) ([]byte, uint64, uint64, error) {
	maxBytes = min(maxBytes, uint64(maxGoSymbolBytes))
	if maxBytes < goSymbolFixedBytes {
		return nil, 0, 0, fmt.Errorf("%w: Go pclntab parser exceeds configured budget", errELFSymbolLimit)
	}
	section := file.Section(".gopclntab")
	if section == nil {
		section = file.Section(".data.rel.ro.gopclntab")
	}
	if section != nil {
		if section.Flags&elf.SHF_COMPRESSED != 0 || section.Size > maxBytes-goSymbolFixedBytes {
			return nil, 0, 0, fmt.Errorf("%w: Go pclntab is compressed or exceeds metadata budget", errELFSymbolLimit)
		}
		data, err := section.Data()
		if err != nil {
			return nil, 0, 0, err
		}
		if err := ctx.Err(); err != nil {
			return nil, 0, 0, err
		}
		return data, section.Addr, 0, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, 0, err
	}
	symbols, err := ReadELFSymbols(ctx, file, elf.SHT_SYMTAB,
		min(maxGoELFMetadataBytes, maxBytes), maxGoELFSymbols, func(name string) bool {
			return name == "runtime.pclntab" || name == "runtime.epclntab"
		})
	if err != nil && !errors.Is(err, elf.ErrNoSymbols) {
		return nil, 0, 0, fmt.Errorf("read ELF symbols: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, 0, err
	}
	var start, end uint64
	var startFound, endFound bool
	var scannedBytes uint64
	if err == nil {
		section := file.SectionByType(elf.SHT_SYMTAB)
		scannedBytes = section.Size + file.Sections[section.Link].Size
	}
	for _, symbol := range symbols {
		// ReadELFSymbols bounds and charges these same retained-name costs.
		scannedBytes += uint64(len(symbol.Name) + 128)
		switch symbol.Name {
		case "runtime.pclntab":
			start, startFound = symbol.Value, true
		case "runtime.epclntab":
			end, endFound = symbol.Value, true
		}
	}
	if scannedBytes > maxBytes-goSymbolFixedBytes {
		return nil, 0, 0, fmt.Errorf("%w: ELF symbol lookup leaves insufficient Go metadata budget", errELFSymbolLimit)
	}
	maxBytes -= scannedBytes
	if !startFound || !endFound {
		// An unrelated .data.rel.ro must not shadow trustworthy ELF symbol
		// bounds. Scan only when those bounds are absent, not on parse errors.
		if section := file.Section(".data.rel.ro"); section != nil {
			data, address, prefixBytes, err := readMergedPCLN(ctx, file, section, maxBytes)
			return data, address, scannedBytes + prefixBytes, err
		}
		return nil, 0, 0, fmt.Errorf("Go pclntab section and runtime symbol range not found")
	}
	if end <= start {
		return nil, 0, 0, fmt.Errorf("invalid pclntab range %#x-%#x", start, end)
	}
	if end-start > maxBytes-goSymbolFixedBytes {
		return nil, 0, 0, fmt.Errorf("%w: Go pclntab exceeds metadata budget", errELFSymbolLimit)
	}
	data, err := ReadELFVirtualRange(file, start, end-start, min(maxGoELFMetadataBytes, maxBytes))
	if err != nil {
		return nil, 0, 0, err
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, 0, err
	}
	return data, start, scannedBytes, nil
}
