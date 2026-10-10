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
	"fmt"
)

const (
	maxGoELFMetadataBytes = 64 << 20
	maxGoELFSymbols       = 1 << 20
)

// ReadGoTable reads and validates Go function metadata without taking ownership of file.
// Returned tables contain link-time PCs; callers own relocation and return-PC handling.
func ReadGoTable(ctx context.Context, file *elf.File) (*gosym.Table, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pcln, err := readPCLN(ctx, file)
	if err != nil {
		return nil, err
	}
	if err := validatePCLN(ctx, pcln, file.ByteOrder); err != nil {
		return nil, err
	}
	textStart, err := goTextStart(file, pcln)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Supported Go releases (1.18+) keep symbols in pclntab. Do not parse
	// obsolete .gosymtab records, whose names can expand during decoding.
	table, err := gosym.NewTable(nil, gosym.NewLineTable(pcln, textStart))
	if err != nil {
		return nil, fmt.Errorf("parse Go symbol table: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return table, nil
}

const (
	maxGoSymbolEntries = 1 << 18
	maxGoSymbolNames   = 16 << 20
	maxGoSymbolName    = 4 << 10
	maxGoSymbolBytes   = 64 << 20
	maxGoPCValueBytes  = 64 << 10
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
func validatePCLN(ctx context.Context, data []byte, order binary.ByteOrder) error {
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
	previous := uint64(headerSize)
	for index := 3; index <= 7; index++ {
		offset := word(index)
		if offset < previous || offset > uint64(len(data)) {
			return fmt.Errorf("Go pclntab table offset is out of bounds")
		}
		previous = offset
	}
	functions := data[word(7):]
	if len(functions) < 44 || nfunc*8+4 > uint64(len(functions)) {
		return fmt.Errorf("Go pclntab function table is truncated")
	}
	names := data[word(3):word(4)]
	remaining := maxGoSymbolNames
	checkName := func(table []byte, offset uint64) (int, error) {
		if offset >= uint64(len(table)) {
			return 0, fmt.Errorf("Go pclntab name offset is out of bounds")
		}
		tail := table[offset:]
		if len(tail) > maxGoSymbolName+1 {
			tail = tail[:maxGoSymbolName+1]
		}
		size := bytes.IndexByte(tail, 0)
		if size < 0 || size+1 > remaining {
			return 0, fmt.Errorf("Go pclntab names exceed safety limit")
		}
		remaining -= size + 1
		// Charge each decoded name, even when offsets overlap, with room
		// for allocator rounding of short strings.
		charge := uint64(size + 16)
		if charge > budget {
			return 0, fmt.Errorf("Go symbol metadata exceeds total memory budget")
		}
		budget -= charge
		return size + 1, nil
	}
	for index := uint64(0); index < nfunc; index++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		offset := uint64(order.Uint32(functions[index*8+4:]))
		if offset < nfunc*8+4 || offset > uint64(len(functions))-44 {
			return fmt.Errorf("Go pclntab function record is out of bounds")
		}
		if _, err := checkName(names, uint64(order.Uint32(functions[offset+4:]))); err != nil {
			return err
		}
	}
	files := data[word(5):word(6)]
	position := uint64(0)
	fileStarts := make(map[uint32]struct{}, nfile)
	for index := uint64(0); index < nfile; index++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		size, err := checkName(files, position)
		if err != nil {
			return err
		}
		fileStarts[uint32(position)] = struct{}{}
		position += uint64(size)
	}
	return validateGoFileReferences(ctx, data[word(4):word(5)], data[word(6):word(7)], functions, nfunc, fileStarts, order, uint32(data[6]))
}

// Every compilation-unit reference must name a validated string start. Checking
// only the declared sequential filenames leaves trailing and interior strings
// reachable through PC-to-file tables.
func validateGoFileReferences(ctx context.Context, units, pcdata, functions []byte,
	nfunc uint64, fileStarts map[uint32]struct{}, order binary.ByteOrder, quantum uint32,
) error {
	if len(units)%4 != 0 {
		return fmt.Errorf("Go compilation-unit table is misaligned")
	}
	for offset := 0; offset < len(units); offset += 4 {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := order.Uint32(units[offset:])
		if name != ^uint32(0) {
			if _, ok := fileStarts[name]; !ok {
				return fmt.Errorf("Go compilation-unit filename offset is not a validated string start")
			}
		}
	}
	// Shared tables are decoded once. A total byte budget also bounds overlapping
	// encodings, which otherwise permit quadratic validation work.
	budget := maxGoSymbolBytes
	decoded := make(map[uint32]int32)
	for index := uint64(0); index < nfunc; index++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		record := functions[order.Uint32(functions[index*8+4:]):]
		for _, field := range []int{20, 24} {
			offset := order.Uint32(record[field:])
			maximum, ok := decoded[offset]
			if !ok {
				var err error
				maximum, err = validateGoPCValues(ctx, pcdata, offset, quantum, &budget)
				if err != nil {
					return fmt.Errorf("Go PC-value offset %d: %w", offset, err)
				}
				decoded[offset] = maximum
			}
			// Offset zero is the no-PC-data sentinel and can have no CU.
			// Its fallback decoding is bounded above; every filename in units
			// was validated independently, including references it may reach.
			if field == 20 && offset != 0 && maximum >= 0 {
				unit := uint64(order.Uint32(record[32:])) + uint64(maximum)
				if unit >= uint64(len(units)/4) {
					return fmt.Errorf("Go PC-to-file compilation-unit index is out of bounds")
				}
			}
		}
	}
	return nil
}

func validateGoPCValues(ctx context.Context, data []byte, offset, quantum uint32, budget *int) (int32, error) {
	if uint64(offset) >= uint64(len(data)) {
		return 0, fmt.Errorf("Go PC-value table offset is out of bounds")
	}
	data = data[offset:]
	consumed := 0
	read := func() (uint32, error) {
		var value uint32
		for shift := uint(0); shift < 35; shift += 7 {
			if len(data) == 0 || *budget == 0 || consumed >= maxGoPCValueBytes {
				return 0, fmt.Errorf("Go PC-value table exceeds safety limit")
			}
			b := data[0]
			data = data[1:]
			consumed++
			*budget--
			if shift == 28 && b > 15 {
				return 0, fmt.Errorf("Go PC-value varint overflows")
			}
			value |= uint32(b&127) << shift
			if b < 128 {
				return value, nil
			}
		}
		return 0, fmt.Errorf("Go PC-value varint overflows")
	}
	value, maximum := int32(-1), int32(-1)
	for first := true; ; {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		delta, err := read()
		if err != nil {
			return 0, err
		}
		if delta == 0 && !first {
			return maximum, nil
		}
		value += int32(delta>>1) ^ -int32(delta&1)
		if value > maximum {
			maximum = value
		}
		advance, err := read()
		if err != nil {
			return 0, err
		}
		if advance > ^uint32(0)/quantum {
			return 0, fmt.Errorf("Go PC-value advance is invalid")
		}
		if advance != 0 {
			first = false
		}
	}
}

// goTextStart returns the link-time start of the Go text section, which the
// pclntab functab offsets are relative to. Go 1.26 removed the textStart field
// from the pcHeader (it requires a relocation and is now a placeholder), so the
// ELF .text section address is used instead; it equals the pcHeader value for
// every supported release.
func goTextStart(file *elf.File, pcln []byte) (uint64, error) {
	if section := file.Section(".text"); section != nil {
		return section.Addr, nil
	}
	return pclnTextStart(pcln, file.ByteOrder)
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

func readPCLN(ctx context.Context, file *elf.File) ([]byte, error) {
	section := file.Section(".gopclntab")
	if section == nil {
		section = file.Section(".data.rel.ro.gopclntab")
	}
	if section != nil {
		if section.Size > maxGoSymbolBytes-goSymbolFixedBytes {
			return nil, fmt.Errorf("Go pclntab exceeds total memory budget")
		}
		data, err := section.Data()
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return data, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	symbols, err := ReadELFSymbols(ctx, file, elf.SHT_SYMTAB,
		maxGoELFMetadataBytes, maxGoELFSymbols, func(name string) bool {
			return name == "runtime.pclntab" || name == "runtime.epclntab"
		})
	if err != nil {
		return nil, fmt.Errorf("read ELF symbols: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var start, end uint64
	var startFound, endFound bool
	for _, symbol := range symbols {
		switch symbol.Name {
		case "runtime.pclntab":
			start, startFound = symbol.Value, true
		case "runtime.epclntab":
			end, endFound = symbol.Value, true
		}
	}
	if !startFound || !endFound {
		return nil, fmt.Errorf("Go pclntab section and runtime symbol range not found")
	}
	if end <= start {
		return nil, fmt.Errorf("invalid pclntab range %#x-%#x", start, end)
	}
	if end-start > maxGoSymbolBytes-goSymbolFixedBytes {
		return nil, fmt.Errorf("Go pclntab exceeds total memory budget")
	}
	data, err := ReadELFVirtualRange(file, start, end-start, maxGoELFMetadataBytes)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}
