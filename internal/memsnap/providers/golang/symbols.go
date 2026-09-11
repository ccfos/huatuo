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
	"bytes"
	"context"
	"debug/elf"
	"debug/gosym"
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/ccfos/huatuo/internal/memsnap"
)

// symbolizer resolves Go PCs from pclntab and remains usable after the
// profiled process exits.
type symbolizer struct {
	table    *gosym.Table
	loadBias uint64
}

// newSymbolizer loads Go symbol metadata from an executable. loadBias is
// subtracted from runtime PCs for PIE binaries.
func newSymbolizer(ctx context.Context, executable string, loadBias uint64,
	table *gosym.Table,
) (*symbolizer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if table != nil {
		return &symbolizer{table: table, loadBias: loadBias}, nil
	}
	executableFile, err := os.Open(executable)
	if err != nil {
		return nil, fmt.Errorf("open executable %q: %w", executable, err)
	}
	defer executableFile.Close()
	file, err := memsnap.ReadELFMetadata(ctx, executableFile)
	if err != nil {
		return nil, fmt.Errorf("read executable ELF %q: %w", executable, err)
	}
	defer file.Close()

	table, err = newGoSymbolTable(ctx, file)
	if err != nil {
		return nil, fmt.Errorf("parse Go symbol table from %q: %w", executable, err)
	}
	return &symbolizer{table: table, loadBias: loadBias}, nil
}

func newGoSymbolTable(ctx context.Context, file *elf.File) (*gosym.Table, error) {
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
	if len(functions) < 16 || nfunc*8+4 > uint64(len(functions)) {
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
	return nil
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
	symbols, err := memsnap.ReadELFSymbols(ctx, file, elf.SHT_SYMTAB,
		maxELFMetadataBytes, maxELFSymbols, func(name string) bool {
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
	data, err := readVirtualRange(file, start, end-start)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

func readVirtualRange(file *elf.File, address, size uint64) ([]byte, error) {
	if size > maxELFMetadataBytes {
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

// resolve resolves one runtime PC to a Go function name.
func (s *symbolizer) resolve(runtimePC uint64) string {
	if s == nil || s.table == nil || runtimePC <= s.loadBias {
		return ""
	}
	// runtime.MemProfile stacks contain return PCs. Move into the call
	// instruction so boundary PCs are attributed to the allocating function.
	function := s.table.PCToFunc(runtimePC - s.loadBias - 1)
	if function == nil {
		return ""
	}
	return function.Name
}
