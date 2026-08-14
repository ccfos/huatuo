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
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/procfs"
)

func captureSymbolLogs(t *testing.T, level string) *bytes.Buffer {
	t.Helper()
	originalLevel := log.GetLevel()
	var output bytes.Buffer
	log.SetOutput(&output)
	log.SetLevel(level)
	t.Cleanup(func() {
		log.SetOutput(os.Stdout)
		log.SetLevel(originalLevel.String())
	})
	return &output
}

func writeKallsymsFixture(t *testing.T, lines []string) string {
	t.Helper()
	fixturePath := filepath.Join(t.TempDir(), "kallsyms")
	content := strings.Join(lines, "\n")
	if len(lines) > 0 {
		content += "\n"
	}
	mustWriteFile(t, fixturePath, content)
	return fixturePath
}

func newSectionSet(entries ...procfs.ProcMap) sections {
	sectionSet := make(sections, 0, len(entries))
	for _, entry := range entries {
		entryCopy := entry
		sectionSet = append(sectionSet, &entryCopy)
	}
	return sectionSet
}

func TestSymbolString(t *testing.T) {
	tests := []struct {
		name         string
		input        symbol
		wantContains []string
	}{
		{
			name:         "includes-name-and-module",
			input:        symbol{Addr: 0xffffffff81000000, Name: "kernel_sched_tick", Module: "[kernel]"},
			wantContains: []string{"kernel_sched_tick", "[kernel]"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.input.String()
			for _, requiredText := range tt.wantContains {
				if !strings.Contains(got, requiredText) {
					t.Errorf("String(): got %q, want contains %q", got, requiredText)
				}
			}
		})
	}
}

func TestSymbolsSort(t *testing.T) {
	tests := []struct {
		name      string
		input     symbols
		wantOrder []uint64
	}{
		{
			name: "sort-by-address-ascending",
			input: symbols{
				{Addr: 0x3000, Name: "do_sys_open"},
				{Addr: 0x1000, Name: "kernel_entry"},
				{Addr: 0x2000, Name: "kernel_sched_tick"},
			},
			wantOrder: []uint64{0x1000, 0x2000, 0x3000},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.input.sort()
			for index := range tt.wantOrder {
				if tt.input[index].Addr != tt.wantOrder[index] {
					t.Errorf("sort[%d]: got 0x%x, want 0x%x", index, tt.input[index].Addr, tt.wantOrder[index])
				}
			}
		})
	}
}

func TestSymbolsResolve(t *testing.T) {
	table := symbols{
		{Addr: 0x1000, Size: 0, Name: "kernel_sched_tick"},
		{Addr: 0x2000, Size: 0x100, Name: "user_func_malloc"},
	}
	tests := []struct {
		name     string
		key      uint64
		wantName string
	}{
		{name: "kernel-style-size-zero-resolves-any-offset", key: 0x1800, wantName: "kernel_sched_tick"},
		{name: "user-style-in-range-resolves", key: 0x20ff, wantName: "user_func_malloc"},
		{name: "user-style-end-exclusive", key: 0x2100, wantName: ""},
		{name: "user-style-overflowing-end-does-not-wrap", key: math.MaxUint64, wantName: ""},
		{name: "below-first-symbol", key: 0x0500, wantName: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := table.resolve(tt.key)
			if got != tt.wantName {
				t.Errorf("resolve(0x%x): got %q, want %q", tt.key, got, tt.wantName)
			}
		})
	}
}

func TestSectionsSort(t *testing.T) {
	tests := []struct {
		name      string
		input     sections
		wantOrder []uintptr
	}{
		{
			name: "sort-by-start-address-ascending",
			input: newSectionSet(
				procfs.ProcMap{Pathname: "libm.so", StartAddr: 0x9000, EndAddr: 0xa000},
				procfs.ProcMap{Pathname: ".text", StartAddr: 0x1000, EndAddr: 0x2000},
				procfs.ProcMap{Pathname: "libc.so", StartAddr: 0x5000, EndAddr: 0x6000},
			),
			wantOrder: []uintptr{0x1000, 0x5000, 0x9000},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.input.sort()
			for index := range tt.wantOrder {
				if tt.input[index].StartAddr != tt.wantOrder[index] {
					t.Errorf("sort[%d]: got 0x%x, want 0x%x", index, tt.input[index].StartAddr, tt.wantOrder[index])
				}
			}
		})
	}
}

func TestSectionsFindBaseAddr(t *testing.T) {
	// Simulate a PIE library with 5 segments: r--p(offset=0), r-xp(offset=0x1000), ...
	piePath := "/usr/lib/libhuatuo-pie.so"
	libcPath := "/usr/lib/libc.so"
	sectionSet := newSectionSet(
		procfs.ProcMap{Pathname: piePath, StartAddr: 0x6553f8937000, EndAddr: 0x6553f8938000, Offset: 0x0000},
		procfs.ProcMap{Pathname: piePath, StartAddr: 0x6553f8938000, EndAddr: 0x6553f8939000, Offset: 0x1000},
		procfs.ProcMap{Pathname: piePath, StartAddr: 0x6553f8939000, EndAddr: 0x6553f893a000, Offset: 0x2000},
		procfs.ProcMap{Pathname: libcPath, StartAddr: 0x7f0000100000, EndAddr: 0x7f0000200000, Offset: 0x0000},
		procfs.ProcMap{Pathname: libcPath, StartAddr: 0x7f0000200000, EndAddr: 0x7f0000300000, Offset: 0x100000},
	)
	sectionSet.sort()

	tests := []struct {
		name     string
		pathname string
		wantAddr uint64
		wantOK   bool
	}{
		{
			name:     "pie-library-base-from-first-segment",
			pathname: piePath,
			// base = StartAddr(0x6553f8937000) - Offset(0) = 0x6553f8937000
			wantAddr: 0x6553f8937000,
			wantOK:   true,
		},
		{
			name:     "libc-base-from-first-segment",
			pathname: libcPath,
			wantAddr: 0x7f0000100000,
			wantOK:   true,
		},
		{
			name:     "unknown-library-not-found",
			pathname: "/usr/lib/libnotfound.so",
			wantAddr: 0,
			wantOK:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAddr, gotOK := sectionSet.findBaseAddr(tt.pathname)
			if gotOK != tt.wantOK {
				t.Fatalf("findBaseAddr(%q): ok=%v, want %v", tt.pathname, gotOK, tt.wantOK)
			}
			if gotAddr != tt.wantAddr {
				t.Errorf("findBaseAddr(%q): got 0x%x, want 0x%x", tt.pathname, gotAddr, tt.wantAddr)
			}
		})
	}
}

func TestSectionsFindBaseAddrNonZeroOffset(t *testing.T) {
	// Edge case: first segment has non-zero offset (unusual but defensive).
	// base = StartAddr - Offset
	sectionSet := newSectionSet(
		procfs.ProcMap{Pathname: "/usr/lib/liboffset.so", StartAddr: 0x8000, EndAddr: 0x9000, Offset: 0x2000},
		procfs.ProcMap{Pathname: "/usr/lib/liboffset.so", StartAddr: 0x9000, EndAddr: 0xa000, Offset: 0x3000},
	)
	sectionSet.sort()

	gotAddr, gotOK := sectionSet.findBaseAddr("/usr/lib/liboffset.so")
	if !gotOK {
		t.Fatalf("findBaseAddr: got ok=false, want true")
	}
	// base = 0x8000 - 0x2000 = 0x6000
	if gotAddr != 0x6000 {
		t.Errorf("findBaseAddr: got 0x%x, want 0x6000", gotAddr)
	}
}

func TestSectionsFind(t *testing.T) {
	sectionSet := newSectionSet(
		procfs.ProcMap{Pathname: ".text", StartAddr: 0x1000, EndAddr: 0x2000},
		procfs.ProcMap{Pathname: "/usr/lib/libpthread.so", StartAddr: 0x5000, EndAddr: 0x6000},
		procfs.ProcMap{Pathname: "", StartAddr: 0x7000, EndAddr: 0x7100},
	)
	sectionSet.sort()
	tests := []struct {
		name     string
		addr     uint64
		wantPath string
		wantNil  bool
	}{
		{name: "find-text-section", addr: 0x1500, wantPath: ".text"},
		{name: "find-library-section", addr: 0x5800, wantPath: "/usr/lib/libpthread.so"},
		{name: "end-address-exclusive", addr: 0x2000, wantNil: true},
		{name: "blank-path-filtered", addr: 0x7001, wantNil: true},
		{name: "gap-address", addr: 0x3000, wantNil: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sectionSet.find(tt.addr)
			if tt.wantNil {
				if got != nil {
					t.Errorf("find(0x%x): got %+v, want nil", tt.addr, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("find(0x%x): got nil, want pathname %q", tt.addr, tt.wantPath)
			}
			if got.Pathname != tt.wantPath {
				t.Errorf("find(0x%x): got pathname %q, want %q", tt.addr, got.Pathname, tt.wantPath)
			}
		})
	}
}

func TestResolveStack(t *testing.T) {
	lookup := map[uint64]string{
		0x1000: "kernel_entry",
		0x2000: "kernel_sched_tick",
		0x3000: "do_sys_open",
	}
	resolve := func(addr uint64) string { return lookup[addr] }
	tests := []struct {
		name  string
		stack []uint64
		want  []string
	}{
		{
			name:  "forward-order-over-full-stack",
			stack: []uint64{0x1000, 0x2000, 0x3000},
			want:  []string{"kernel_entry", "kernel_sched_tick", "do_sys_open"},
		},
		{name: "stop-at-first-zero", stack: []uint64{0x1000, 0x0, 0x3000}, want: []string{"kernel_entry"}},
		{
			name:  "unknown-fallback-for-unresolved-address",
			stack: []uint64{0x1000, 0x9999, 0x2000},
			want:  []string{"kernel_entry", "unknown", "kernel_sched_tick"},
		},
		{name: "empty-stack", stack: []uint64{}, want: []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveStack(tt.stack, resolve).strings
			if !slices.Equal(got, tt.want) {
				t.Errorf("resolveStack strings: got %v, want %v", got, tt.want)
			}
		})
	}

	bytesFrames := resolveStack([]uint64{0x1000, 0x0, 0x2000}, resolve, outTypeBytes).bytes
	wantBytes := []string{"kernel_entry"}
	if !slices.Equal(bytesFramesToStrings(bytesFrames), wantBytes) {
		t.Errorf("resolveStack bytes: got %v, want %v", bytesFramesToStrings(bytesFrames), wantBytes)
	}
}

func TestSearchFloorIndex(t *testing.T) {
	values := []uint64{0x1000, 0x2000, 0x3000}
	tests := []struct {
		name      string
		key       uint64
		wantIndex int
	}{
		{name: "exact-match", key: 0x2000, wantIndex: 1},
		{name: "between-values", key: 0x2800, wantIndex: 1},
		{name: "below-minimum", key: 0x0100, wantIndex: -1},
		{name: "above-maximum", key: 0x9000, wantIndex: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := searchFloorIndex(len(values), func(index int) bool {
				return values[index] > tt.key
			})
			if got != tt.wantIndex {
				t.Errorf("searchFloorIndex key=0x%x: got %d, want %d", tt.key, got, tt.wantIndex)
			}
		})
	}
}

func TestSymbolsFloorSym(t *testing.T) {
	table := symbols{
		{Addr: 0x1000, Name: "kernel_entry"},
		{Addr: 0x2000, Name: "kernel_sched_tick"},
		{Addr: 0x3000, Name: "do_sys_open"},
	}
	tests := []struct {
		name      string
		key       uint64
		wantName  string
		wantIsNil bool
	}{
		{name: "exact-match-first", key: 0x1000, wantName: "kernel_entry"},
		{name: "offset-second", key: 0x2400, wantName: "kernel_sched_tick"},
		{name: "beyond-last", key: 0x9000, wantName: "do_sys_open"},
		{name: "below-all", key: 0x0500, wantIsNil: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := table.floorSym(tt.key)
			if tt.wantIsNil {
				if got != nil {
					t.Errorf("floorSym(0x%x): got %+v, want nil", tt.key, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("floorSym(0x%x): got nil, want %q", tt.key, tt.wantName)
			}
			if got.Name != tt.wantName {
				t.Errorf("floorSym(0x%x): got %q, want %q", tt.key, got.Name, tt.wantName)
			}
		})
	}

	empty := symbols{}
	if got := empty.floorSym(0x1000); got != nil {
		t.Errorf("empty floorSym: got %+v, want nil", got)
	}
}

func TestParseKallsymsLine(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		wantOK     bool
		wantAddr   uint64
		wantName   string
		wantModule string
	}{
		{
			name:       "global-text-symbol",
			line:       "ffffffff81000000 T kernel_sched_tick",
			wantOK:     true,
			wantAddr:   0xffffffff81000000,
			wantName:   "kernel_sched_tick",
			wantModule: "[kernel]",
		},
		{
			name:       "module-local-text-symbol",
			line:       "ffffffffc0100000 t nf_conntrack_in [nf_conntrack]",
			wantOK:     true,
			wantAddr:   0xffffffffc0100000,
			wantName:   "nf_conntrack_in",
			wantModule: "[nf_conntrack]",
		},
		{name: "data-symbol-rejected", line: "ffffffff81200000 D kernel_percpu_data", wantOK: false},
		{name: "malformed-address-rejected", line: "ZZZZZZZZ T kernel_bad_addr", wantOK: false},
		{name: "too-few-fields-rejected", line: "ffffffff81000000 T", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseKallsymsLine(tt.line)
			if ok != tt.wantOK {
				t.Fatalf("parseKallsymsLine(%q): ok=%v, want %v", tt.line, ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if got.Addr != tt.wantAddr || got.Name != tt.wantName || got.Module != tt.wantModule {
				t.Errorf("parseKallsymsLine(%q): got %+v, want Addr=0x%x Name=%q Module=%q", tt.line, got, tt.wantAddr, tt.wantName, tt.wantModule)
			}
		})
	}
}

func TestScanKallsyms(t *testing.T) {
	tests := []struct {
		name      string
		lines     []string
		wantCount int
	}{
		{
			name: "filter-non-text-symbols",
			lines: []string{
				"ffffffff81000000 T kernel_sched_tick",
				"ffffffff81100000 D kernel_percpu_data",
				"ffffffff81200000 t do_sys_open",
			},
			wantCount: 2,
		},
		{name: "empty-file", lines: []string{}, wantCount: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixturePath := writeKallsymsFixture(t, tt.lines)
			got, err := scanKallsyms(fixturePath, 16)
			if err != nil {
				t.Fatalf("scanKallsyms(%q): %v", fixturePath, err)
			}
			if len(got) != tt.wantCount {
				t.Errorf("scanKallsyms(%q): got %d symbols, want %d", fixturePath, len(got), tt.wantCount)
			}
		})
	}
}

func TestScanKallsymsNotFound(t *testing.T) {
	_, err := scanKallsyms("/proc/kallsyms-huatuo-not-found", 16)
	if err == nil {
		t.Errorf("scanKallsyms not-found: got nil error, want non-nil")
	}
}

func TestElfSymbols(t *testing.T) {
	executablePath, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	elfFile, err := elf.Open(executablePath)
	if err != nil {
		t.Fatalf("elf.Open(%q): %v", executablePath, err)
	}
	defer elfFile.Close()

	got, err := elfSymbols(elfFile, DefaultELFSymbolLimits())
	if err != nil {
		t.Fatalf("elfSymbols(%q): %v", executablePath, err)
	}
	if len(got) == 0 {
		t.Errorf("elfSymbols(%q): got 0 symbols, want >0", executablePath)
	}
	for index := 1; index < len(got); index++ {
		if got[index-1].Addr > got[index].Addr {
			t.Errorf("elfSymbols sort order: got[%d]=0x%x > got[%d]=0x%x", index-1, got[index-1].Addr, index, got[index].Addr)
		}
	}
}

type elf64SymbolTableFixture struct {
	typ         elf.SectionType
	stringTable []byte
	nameOffsets []uint32
	symbolTypes []elf.SymType
}

func alignUp(value, alignment int) int {
	return (value + alignment - 1) &^ (alignment - 1)
}

func encodeELFStruct(t *testing.T, value any) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := binary.Write(&encoded, binary.LittleEndian, value); err != nil {
		t.Fatalf("binary.Write(%T): %v", value, err)
	}
	return encoded.Bytes()
}

func newELF64SymbolFixture(t *testing.T, tables ...elf64SymbolTableFixture) *elf.File {
	t.Helper()
	type encodedTable struct {
		fixture             elf64SymbolTableFixture
		stringsOffset       int
		symbolsOffset       int
		encodedSymbols      []byte
		stringsSectionIndex uint32
	}

	headerSize := binary.Size(elf.Header64{})
	sectionSize := binary.Size(elf.Section64{})
	offset := headerSize
	encodedTables := make([]encodedTable, 0, len(tables))
	for tableIndex, table := range tables {
		if len(table.stringTable) == 0 || table.stringTable[0] != 0 {
			t.Fatal("ELF fixture string table must begin with NUL")
		}
		if len(table.symbolTypes) != 0 && len(table.symbolTypes) != len(table.nameOffsets) {
			t.Fatal("ELF fixture symbol types must match name offsets")
		}
		stringsOffset := offset
		offset = alignUp(offset+len(table.stringTable), 8)

		var symbols bytes.Buffer
		if err := binary.Write(&symbols, binary.LittleEndian, elf.Sym64{}); err != nil {
			t.Fatalf("binary.Write(null symbol): %v", err)
		}
		for symbolIndex, nameOffset := range table.nameOffsets {
			symbolType := elf.STT_FUNC
			if len(table.symbolTypes) != 0 {
				symbolType = table.symbolTypes[symbolIndex]
			}
			sym := elf.Sym64{
				Name:  nameOffset,
				Info:  byte(symbolType),
				Shndx: 1,
				Value: 0x1000 + uint64(symbolIndex)*0x10,
				Size:  0x10,
			}
			if err := binary.Write(&symbols, binary.LittleEndian, sym); err != nil {
				t.Fatalf("binary.Write(symbol): %v", err)
			}
		}
		symbolsOffset := offset
		offset = alignUp(offset+symbols.Len(), 8)
		encodedTables = append(encodedTables, encodedTable{
			fixture:             table,
			stringsOffset:       stringsOffset,
			symbolsOffset:       symbolsOffset,
			encodedSymbols:      symbols.Bytes(),
			stringsSectionIndex: uint32(1 + tableIndex*2),
		})
	}

	sectionHeadersOffset := offset
	sectionCount := 1 + len(encodedTables)*2
	image := make([]byte, sectionHeadersOffset+sectionCount*sectionSize)
	ident := [elf.EI_NIDENT]byte{}
	copy(ident[:], elf.ELFMAG)
	ident[elf.EI_CLASS] = byte(elf.ELFCLASS64)
	ident[elf.EI_DATA] = byte(elf.ELFDATA2LSB)
	ident[elf.EI_VERSION] = byte(elf.EV_CURRENT)
	header := elf.Header64{
		Ident:     ident,
		Type:      uint16(elf.ET_REL),
		Machine:   uint16(elf.EM_X86_64),
		Version:   uint32(elf.EV_CURRENT),
		Shoff:     uint64(sectionHeadersOffset),
		Ehsize:    uint16(headerSize),
		Shentsize: uint16(sectionSize),
		Shnum:     uint16(sectionCount),
	}
	copy(image, encodeELFStruct(t, header))

	for tableIndex := range encodedTables {
		table := &encodedTables[tableIndex]
		copy(image[table.stringsOffset:], table.fixture.stringTable)
		copy(image[table.symbolsOffset:], table.encodedSymbols)
		stringsHeader := elf.Section64{
			Type:      uint32(elf.SHT_STRTAB),
			Off:       uint64(table.stringsOffset),
			Size:      uint64(len(table.fixture.stringTable)),
			Addralign: 1,
		}
		symbolsHeader := elf.Section64{
			Type:      uint32(table.fixture.typ),
			Off:       uint64(table.symbolsOffset),
			Size:      uint64(len(table.encodedSymbols)),
			Link:      table.stringsSectionIndex,
			Addralign: 8,
			Entsize:   elf.Sym64Size,
		}
		stringsHeaderOffset := sectionHeadersOffset + (1+tableIndex*2)*sectionSize
		symbolsHeaderOffset := stringsHeaderOffset + sectionSize
		copy(image[stringsHeaderOffset:], encodeELFStruct(t, stringsHeader))
		copy(image[symbolsHeaderOffset:], encodeELFStruct(t, symbolsHeader))
	}

	f, err := elf.NewFile(bytes.NewReader(image))
	if err != nil {
		t.Fatalf("elf.NewFile(real fixture): %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func newELF32SymbolFixture(t *testing.T, stringTable []byte, nameOffset uint32) *elf.File {
	t.Helper()
	headerSize := binary.Size(elf.Header32{})
	sectionSize := binary.Size(elf.Section32{})
	stringsOffset := headerSize
	symbolsOffset := alignUp(stringsOffset+len(stringTable), 4)
	symbols := append(encodeELFStruct(t, elf.Sym32{}), encodeELFStruct(t, elf.Sym32{
		Name:  nameOffset,
		Info:  byte(elf.STT_FUNC),
		Shndx: 1,
		Value: 0x1000,
		Size:  0x10,
	})...)
	sectionHeadersOffset := alignUp(symbolsOffset+len(symbols), 4)
	image := make([]byte, sectionHeadersOffset+3*sectionSize)

	ident := [elf.EI_NIDENT]byte{}
	copy(ident[:], elf.ELFMAG)
	ident[elf.EI_CLASS] = byte(elf.ELFCLASS32)
	ident[elf.EI_DATA] = byte(elf.ELFDATA2LSB)
	ident[elf.EI_VERSION] = byte(elf.EV_CURRENT)
	header := elf.Header32{
		Ident:     ident,
		Type:      uint16(elf.ET_REL),
		Machine:   uint16(elf.EM_386),
		Version:   uint32(elf.EV_CURRENT),
		Shoff:     uint32(sectionHeadersOffset),
		Ehsize:    uint16(headerSize),
		Shentsize: uint16(sectionSize),
		Shnum:     3,
	}
	copy(image, encodeELFStruct(t, header))
	copy(image[stringsOffset:], stringTable)
	copy(image[symbolsOffset:], symbols)
	copy(image[sectionHeadersOffset+sectionSize:], encodeELFStruct(t, elf.Section32{
		Type:      uint32(elf.SHT_STRTAB),
		Off:       uint32(stringsOffset),
		Size:      uint32(len(stringTable)),
		Addralign: 1,
	}))
	copy(image[sectionHeadersOffset+2*sectionSize:], encodeELFStruct(t, elf.Section32{
		Type:      uint32(elf.SHT_SYMTAB),
		Off:       uint32(symbolsOffset),
		Size:      uint32(len(symbols)),
		Link:      1,
		Addralign: 4,
		Entsize:   elf.Sym32Size,
	}))

	f, err := elf.NewFile(bytes.NewReader(image))
	if err != nil {
		t.Fatalf("elf.NewFile(real ELF32 fixture): %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestElfSymbolsParsesRealELF32(t *testing.T) {
	f := newELF32SymbolFixture(t, []byte("\x00func32\x00"), 1)
	limits := ELFSymbolLimits{MaxMetadataBytes: 1024, MaxSymbolCount: 1, MaxNameBytes: 32}

	got, err := elfSymbols(f, limits)
	if err != nil {
		t.Fatalf("elfSymbols(real ELF32): %v", err)
	}
	if len(got) != 1 || got[0].Name != "func32" || got[0].Addr != 0x1000 || got[0].Size != 0x10 {
		t.Fatalf("elfSymbols(real ELF32): got %v, want func32 at 0x1000", got)
	}
}

func TestElfSymbolsBoundsExpandedNamesInRealELF(t *testing.T) {
	const (
		nameSize    = 64 << 10
		symbolCount = 512
	)
	longName := strings.Repeat("x", nameSize)
	stringTable := append([]byte{0}, longName...)
	stringTable = append(stringTable, 0)
	nameOffsets := make([]uint32, symbolCount)
	for i := range nameOffsets {
		nameOffsets[i] = 1
	}
	f := newELF64SymbolFixture(t, elf64SymbolTableFixture{
		typ:         elf.SHT_SYMTAB,
		stringTable: stringTable,
		nameOffsets: nameOffsets,
	})
	limits := ELFSymbolLimits{
		MaxMetadataBytes: 1 << 20,
		MaxSymbolCount:   symbolCount,
		MaxNameBytes:     nameSize,
	}

	state := newELFSymbolParseState(limits)
	got, err := state.parseSource(f, elfSymbolSource{name: "symtab", typ: elf.SHT_SYMTAB})
	if err != nil {
		t.Fatalf("parseSource(real repeated-name ELF): %v", err)
	}
	if len(got) != symbolCount {
		t.Fatalf("parseSource(real repeated-name ELF): got %d symbols, want %d", len(got), symbolCount)
	}
	if state.nameBytes != nameSize {
		t.Fatalf("parseSource(real repeated-name ELF): counted %d expanded name bytes, want %d", state.nameBytes, nameSize)
	}
}

func TestElfSymbolsRejectsDistinctExpandedNamesInRealELF(t *testing.T) {
	const nameSize = 32 << 10
	firstName := strings.Repeat("a", nameSize)
	secondName := strings.Repeat("b", nameSize)
	stringTable := append([]byte{0}, firstName...)
	stringTable = append(stringTable, 0)
	secondOffset := uint32(len(stringTable))
	stringTable = append(stringTable, secondName...)
	stringTable = append(stringTable, 0)
	f := newELF64SymbolFixture(t, elf64SymbolTableFixture{
		typ:         elf.SHT_SYMTAB,
		stringTable: stringTable,
		nameOffsets: []uint32{1, secondOffset},
	})
	limits := ELFSymbolLimits{
		MaxMetadataBytes: 1 << 20,
		MaxSymbolCount:   2,
		MaxNameBytes:     nameSize,
	}

	got, err := elfSymbols(f, limits)
	if !errors.Is(err, ErrELFSymbolLimit) {
		t.Fatalf("elfSymbols(real expanded-name ELF): got error %v, want ErrELFSymbolLimit", err)
	}
	if len(got) != 0 {
		t.Fatalf("elfSymbols(real expanded-name ELF): got %d symbols from rejected source, want 0", len(got))
	}
}

func TestElfSymbolsFiltersBeforeCopyingNamesInRealELF(t *testing.T) {
	const ignoredNameSize = 64 << 10
	ignoredName := strings.Repeat("x", ignoredNameSize)
	stringTable := append([]byte{0}, ignoredName...)
	stringTable = append(stringTable, 0)
	functionOffset := uint32(len(stringTable))
	stringTable = append(stringTable, "kept\x00"...)
	f := newELF64SymbolFixture(t, elf64SymbolTableFixture{
		typ:         elf.SHT_SYMTAB,
		stringTable: stringTable,
		nameOffsets: []uint32{1, functionOffset},
		symbolTypes: []elf.SymType{elf.STT_OBJECT, elf.STT_FUNC},
	})
	limits := ELFSymbolLimits{
		MaxMetadataBytes: 1 << 20,
		MaxSymbolCount:   2,
		MaxNameBytes:     uint64(len("kept")),
	}

	got, err := elfSymbols(f, limits)
	if err != nil {
		t.Fatalf("elfSymbols(real filtered-name ELF): %v", err)
	}
	if len(got) != 1 || got[0].Name != "kept" {
		t.Fatalf("elfSymbols(real filtered-name ELF): got %v, want one kept function", got)
	}
}

func TestElfSymbolsForPCsDoesNotMaterializeLargeStringTable(t *testing.T) {
	const ignoredNameSize = 8 << 20
	stringTable := append([]byte{0}, bytes.Repeat([]byte{'x'}, ignoredNameSize)...)
	stringTable = append(stringTable, 0)
	targetOffset := uint32(len(stringTable))
	stringTable = append(stringTable, "target\x00"...)
	f := newELF64SymbolFixture(t, elf64SymbolTableFixture{
		typ:         elf.SHT_SYMTAB,
		stringTable: stringTable,
		nameOffsets: []uint32{1, targetOffset},
	})
	limits := ELFSymbolLimits{
		MaxMetadataBytes: 9 << 20,
		MaxSymbolCount:   2,
		MaxNameBytes:     32,
		MaxNameLength:    16,
	}

	benchmark := testing.Benchmark(func(b *testing.B) {
		for range b.N {
			got, err := elfSymbolsForPCs(f, []uint64{0x1011}, limits)
			if err != nil || len(got) != 1 || got[0].Name != "target" {
				b.Fatalf("elfSymbolsForPCs: got %v, err %v; want target", got, err)
			}
		}
	})
	t.Logf("address-driven parse: %d bytes/op for an 8 MiB string table", benchmark.AllocedBytesPerOp())
	if allocated := benchmark.AllocedBytesPerOp(); allocated >= 1<<20 {
		t.Fatalf("address-driven parse allocated %d bytes/op for an 8 MiB string table; want < 1 MiB", allocated)
	}
}

func TestElfSymbolsForPCsLimitsSingleName(t *testing.T) {
	f := newELF64SymbolFixture(t, elf64SymbolTableFixture{
		typ:         elf.SHT_SYMTAB,
		stringTable: []byte("\x00name-too-long\x00"),
		nameOffsets: []uint32{1},
	})
	limits := ELFSymbolLimits{
		MaxMetadataBytes: 1024,
		MaxSymbolCount:   1,
		MaxNameBytes:     64,
		MaxNameLength:    4,
	}
	got, err := elfSymbolsForPCs(f, []uint64{0x1001}, limits)
	if !errors.Is(err, ErrELFSymbolLimit) || len(got) != 0 {
		t.Fatalf("single-name limit: got %v, err %v; want no symbols and ErrELFSymbolLimit", got, err)
	}
}

func TestElfSymbolsForPCsReusesNameBudgetAcrossCalls(t *testing.T) {
	f := newELF64SymbolFixture(t, elf64SymbolTableFixture{
		typ:         elf.SHT_SYMTAB,
		stringTable: []byte("\x00target\x00"),
		nameOffsets: []uint32{1, 1},
	})
	state := newELFSymbolParseState(ELFSymbolLimits{
		MaxMetadataBytes: 1024,
		MaxSymbolCount:   2,
		MaxNameBytes:     uint64(len("target")),
		MaxNameLength:    32,
	})
	for _, pc := range []uint64{0x1001, 0x1011} {
		got, err := elfSymbolsForPCsWithState(f, []uint64{pc}, state)
		if err != nil || len(got) != 1 {
			t.Fatalf("elfSymbolsForPCsWithState(%#x): got %v, err %v; want one symbol", pc, got, err)
		}
	}
}

func TestElfSymbolsForPCsPrefersSymtabAndFallsBackForUnresolvedPCs(t *testing.T) {
	f := newELF64SymbolFixture(t,
		elf64SymbolTableFixture{
			typ:         elf.SHT_DYNSYM,
			stringTable: []byte("\x00dynamic\x00"),
			nameOffsets: []uint32{1},
		},
		elf64SymbolTableFixture{
			typ:         elf.SHT_SYMTAB,
			stringTable: []byte("\x00static\x00"),
			nameOffsets: []uint32{1},
		},
	)
	got, err := elfSymbolsForPCs(f, []uint64{0x1001}, DefaultELFSymbolLimits())
	if err != nil || len(got) != 1 || got[0].Name != "static" {
		t.Fatalf("elfSymbolsForPCs: got %v, err %v; want symtab symbol", got, err)
	}
}

func TestElfSymbolsForPCsDoesNotLogMissingSourceAtInfo(t *testing.T) {
	output := captureSymbolLogs(t, "info")
	f := newELF64SymbolFixture(t, elf64SymbolTableFixture{
		typ:         elf.SHT_SYMTAB,
		stringTable: []byte("\x00target\x00"),
		nameOffsets: []uint32{1},
	})
	limits := ELFSymbolLimits{
		MaxMetadataBytes: 1024,
		MaxSymbolCount:   1,
		MaxNameBytes:     32,
		MaxNameLength:    16,
	}

	got, err := elfSymbolsForPCs(f, []uint64{0x1001}, limits)
	if err != nil || len(got) != 1 || got[0].Name != "target" {
		t.Fatalf("elfSymbolsForPCs: got %v, err %v; want target", got, err)
	}
	if strings.Contains(output.String(), "dynsym not available") {
		t.Fatalf("missing optional dynsym logged at info: %s", output.String())
	}
}

func TestElfSymbolsForPCsEmptyPCsReturnsEmpty(t *testing.T) {
	f := newELF64SymbolFixture(t, elf64SymbolTableFixture{
		typ:         elf.SHT_SYMTAB,
		stringTable: []byte("\x00target\x00"),
		nameOffsets: []uint32{1},
	})

	got, err := elfSymbolsForPCs(f, nil, DefaultELFSymbolLimits())
	if err != nil {
		t.Fatalf("elfSymbolsForPCs: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("elfSymbolsForPCs(nil): got %d symbols, want 0", len(got))
	}
}

func TestElfSymbolsSkipsOversizedRealSourceAndKeepsFallback(t *testing.T) {
	oversizedStrings := append([]byte{0}, bytes.Repeat([]byte{'x'}, 4096)...)
	oversizedStrings = append(oversizedStrings, 0)
	f := newELF64SymbolFixture(t,
		elf64SymbolTableFixture{
			typ:         elf.SHT_DYNSYM,
			stringTable: oversizedStrings,
			nameOffsets: []uint32{1},
		},
		elf64SymbolTableFixture{
			typ:         elf.SHT_SYMTAB,
			stringTable: []byte("\x00fallback\x00"),
			nameOffsets: []uint32{1},
		},
	)
	limits := ELFSymbolLimits{
		MaxMetadataBytes: 1024,
		MaxSymbolCount:   2,
		MaxNameBytes:     1024,
	}

	got, err := elfSymbols(f, limits)
	if !errors.Is(err, ErrELFSymbolLimit) {
		t.Fatalf("elfSymbols(real fallback ELF): got error %v, want ErrELFSymbolLimit", err)
	}
	if len(got) != 1 || got[0].Name != "fallback" {
		t.Fatalf("elfSymbols(real fallback ELF): got %v, want one fallback symbol", got)
	}
}

func TestElfSymbolsLimitsRealSymbolCount(t *testing.T) {
	f := newELF64SymbolFixture(t, elf64SymbolTableFixture{
		typ:         elf.SHT_SYMTAB,
		stringTable: []byte("\x00func\x00"),
		nameOffsets: []uint32{1, 1, 1},
	})
	limits := ELFSymbolLimits{
		MaxMetadataBytes: 1024,
		MaxSymbolCount:   2,
		MaxNameBytes:     1024,
	}

	got, err := elfSymbols(f, limits)
	if !errors.Is(err, ErrELFSymbolLimit) {
		t.Fatalf("elfSymbols(real symbol-count ELF): got error %v, want ErrELFSymbolLimit", err)
	}
	if len(got) != 0 {
		t.Fatalf("elfSymbols(real symbol-count ELF): got %d symbols, want 0", len(got))
	}
}

func TestIsLibPath(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{name: "empty-path", input: "", want: false},
		{name: "relative-path", input: "usr/lib/libc.so", want: false},
		{name: "blocked-heap", input: "[heap]", want: false},
		{name: "blocked-dev-zero", input: "/dev/zero", want: false},
		{name: "valid-library", input: "/usr/lib/libc.so", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isLibPath(tt.input)
			if got != tt.want {
				t.Errorf("isLibPath(%q): got %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseMaps(t *testing.T) {
	tmpRoot := setupTempProcRoot(t)
	processID := 1001
	procDir := filepath.Join(tmpRoot, "proc", strconv.Itoa(processID))
	mustMkdirAll(t, procDir)
	mapsContent := strings.Join([]string{
		"7f0000001000-7f0000002000 r-xp 00000000 fd:01 1001 /usr/lib/libc.so",
		"7f0000003000-7f0000004000 r--p 00000000 fd:01 1002 [heap]",
		"7f0000005000-7f0000006000 r-xp 00000000 fd:01 1003 /usr/lib/libm.so",
	}, "\n") + "\n"
	mustWriteFile(t, filepath.Join(procDir, "maps"), mapsContent)

	got, err := parseMaps(uint32(processID))
	if err != nil {
		t.Fatalf("parseMaps(%d): %v", processID, err)
	}
	if len(got) != 3 {
		t.Errorf("parseMaps(%d): got %d maps, want 3", processID, len(got))
	}
	pathnames := make([]string, 0, len(got))
	for _, procMap := range got {
		if procMap == nil {
			t.Errorf("parseMaps(%d): got nil proc map entry", processID)
			continue
		}
		if procMap.StartAddr >= procMap.EndAddr {
			t.Errorf("parseMaps(%d): invalid range [%x,%x)", processID, procMap.StartAddr, procMap.EndAddr)
		}
		pathnames = append(pathnames, procMap.Pathname)
	}
	if !slices.Contains(pathnames, "/usr/lib/libc.so") {
		t.Errorf("parseMaps(%d): got pathnames %v, want contains /usr/lib/libc.so", processID, pathnames)
	}
}

func TestParseMapsNotFound(t *testing.T) {
	setupTempProcRoot(t)
	_, err := parseMaps(uint32(1001))
	if err == nil {
		t.Errorf("parseMaps not-found: got nil error, want non-nil")
	}
}

func TestXfsMountPoints(t *testing.T) {
	tmpRoot := setupTempProcRoot(t)
	selfDir := filepath.Join(tmpRoot, "proc", "self")
	mustMkdirAll(t, selfDir)
	tmpFS := "tmpfs"
	mountInfo := strings.Join([]string{
		"35 23 8:0 / / rw,relatime - xfs /dev/sda1 rw,attr2",
		"36 35 8:0 /var/lib /var/lib rw,relatime - xfs /dev/sda1 rw,attr2",
		fmt.Sprintf("37 35 0:45 / /run rw,nosuid,nodev - %s %s rw,size=1024k", tmpFS, tmpFS),
		"38 35 8:0 / / rw,relatime - xfs /dev/sda1 rw,attr2",
	}, "\n") + "\n"
	mustWriteFile(t, filepath.Join(selfDir, "mountinfo"), mountInfo)

	got, err := xfsMountPoints()
	if err != nil {
		t.Fatalf("xfsMountPoints(): %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("xfsMountPoints(): got %d mounts, want 2", len(got))
	}
	if !slices.Contains(got, "/") || !slices.Contains(got, "/var/lib") {
		t.Errorf("xfsMountPoints(): got %v, want contains / and /var/lib", got)
	}
}

func TestMatchXfsMount(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		xfsMounts []string
		want      string
		wantErr   bool
	}{
		{
			name:      "nested-mount-matches-first",
			path:      "/var/lib/container/rootfs",
			xfsMounts: []string{"/var/lib", "/"},
			want:      "/var/lib",
		},
		{name: "root-mount-matches", path: "/usr/lib/libc.so", xfsMounts: []string{"/"}, want: "/"},
		{name: "no-match", path: "/usr/lib/libc.so", xfsMounts: []string{"/var/lib"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := matchXfsMount(tt.path, tt.xfsMounts)
			if tt.wantErr {
				if err == nil {
					t.Errorf("matchXfsMount(%q): got nil error, want non-nil", tt.path)
				}
				return
			}
			if err != nil {
				t.Fatalf("matchXfsMount(%q): %v", tt.path, err)
			}
			if got != tt.want {
				t.Errorf("matchXfsMount(%q): got %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestLowerDirFromMountInfo(t *testing.T) {
	tmpRoot := setupTempProcRoot(t)
	processID := uint32(1001)
	procDir := filepath.Join(tmpRoot, "proc", strconv.Itoa(int(processID)))
	mustMkdirAll(t, procDir)
	overlayFS := "overlay"
	mountInfo := "35 23 8:0 / / rw,relatime - xfs /dev/sda1 rw,attr2\n" +
		fmt.Sprintf(
			"36 35 0:32 / / rw,relatime - %s %s rw,lowerdir=/layers/base:/layers/final,upperdir=/layers/upper,workdir=/layers/work\n",
			overlayFS,
			overlayFS,
		)
	mustWriteFile(t, filepath.Join(procDir, "mountinfo"), mountInfo)

	got, err := lowerDirFromMountInfo(processID)
	if err != nil {
		t.Fatalf("lowerDirFromMountInfo(%d): %v", processID, err)
	}
	if got != "/layers/final" {
		t.Errorf("lowerDirFromMountInfo(%d): got %q, want %q", processID, got, "/layers/final")
	}
}

func TestLowerDirFromMountInfoNotFound(t *testing.T) {
	tmpRoot := setupTempProcRoot(t)
	processID := uint32(1001)
	procDir := filepath.Join(tmpRoot, "proc", strconv.Itoa(int(processID)))
	mustMkdirAll(t, procDir)
	mountInfo := "35 23 8:0 / / rw,relatime - xfs /dev/sda1 rw,attr2\n"
	mustWriteFile(t, filepath.Join(procDir, "mountinfo"), mountInfo)

	_, err := lowerDirFromMountInfo(processID)
	if err == nil {
		t.Errorf("lowerDirFromMountInfo(%d): got nil error, want non-nil", processID)
	}
}
