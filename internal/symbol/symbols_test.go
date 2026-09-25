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

	"github.com/ccfos/huatuo/internal/procfs"
)

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
		{Addr: math.MaxUint64 - 0x100, Size: 0x200, Name: "overflowing_func"},
	}
	tests := []struct {
		name     string
		key      uint64
		wantName string
	}{
		{name: "zero-size-symbol-does-not-cover-higher-offset", key: 0x1800, wantName: ""},
		{name: "zero-size-symbol-matches-its-address", key: 0x1000, wantName: "kernel_sched_tick"},
		{name: "user-style-in-range-resolves", key: 0x20ff, wantName: "user_func_malloc"},
		{name: "user-style-end-exclusive", key: 0x2100, wantName: ""},
		{name: "overflowing-symbol-end-does-not-wrap", key: math.MaxUint64, wantName: ""},
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

func TestELFSymbolNameBudget(t *testing.T) {
	for _, tc := range []struct {
		name         string
		secondOffset uint32
		firstType    elf.SymType
		nameLimit    uint64
		batches      [][]uint64
		want         []string
		wantLimit    bool
	}{
		{"shared-offset", 1, elf.STT_FUNC, 16, [][]uint64{{0x1001, 0x1011}}, []string{"target", "target"}, false},
		{"distinct-offsets", 8, elf.STT_FUNC, 16, [][]uint64{{0x1001, 0x1011}}, nil, true},
		{"filtered-symbol", 8, elf.STT_OBJECT, 16, [][]uint64{{0x1001, 0x1011}}, []string{"", "otherx"}, false},
		{"single-name-too-long", 1, elf.STT_FUNC, 5, [][]uint64{{0x1001}}, nil, true},
		{"shared-across-calls", 1, elf.STT_FUNC, 16, [][]uint64{{0x1001}, {0x1011}}, []string{"target", "target"}, false},
		{"distinct-across-calls", 8, elf.STT_FUNC, 16, [][]uint64{{0x1001}, {0x1011}}, []string{"target"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			image := readELFFixture(t, "symbols64.elf")
			f := openELFFixture(t, image)
			offset := f.SectionByType(elf.SHT_SYMTAB).Offset + elf.Sym64Size
			image[offset+4] = byte(tc.firstType)
			binary.LittleEndian.PutUint32(image[offset+elf.Sym64Size:], tc.secondOffset)
			f.SectionByType(elf.SHT_DYNSYM).Type = elf.SHT_NULL
			limits := DefaultELFSymbolLimits()
			limits.MaxNameBytes = uint64(len("target"))
			limits.MaxNameLength = tc.nameLimit
			state := newELFSymbolParseState(limits)
			var names []string
			for i, pcs := range tc.batches {
				got, err := elfSymbolsForPCsWithState(f, pcs, state)
				if tc.wantLimit && i == len(tc.batches)-1 {
					if !errors.Is(err, errELFSymbolLimit) || len(got) != 0 {
						t.Fatalf("got %v, %v; want rejected source", got, err)
					}
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				for _, pc := range pcs {
					names = append(names, got.resolve(pc))
				}
			}
			if !slices.Equal(names, tc.want) {
				t.Fatalf("got %v, want %v", names, tc.want)
			}
			if state.nameBytes > limits.MaxNameBytes {
				t.Fatal("name budget exceeded")
			}
			if len(names) > 0 && state.nameBytes != uint64(len("target")) {
				t.Fatalf("charged %d bytes, want one distinct name", state.nameBytes)
			}
		})
	}
}

func TestELFSymbolSources(t *testing.T) {
	t.Run("empty-PCs", func(t *testing.T) {
		got, err := elfSymbolsForPCsWithState(nil, nil, nil)
		if err != nil || len(got) != 0 {
			t.Fatalf("got %v, %v", got, err)
		}
	})
	for _, tc := range []struct {
		name           string
		dynamicStart   uint64
		missingDynamic bool
		pcs            []uint64
		want           []string
	}{
		{"prefer-symtab", 0x1000, false, []uint64{0x1009}, []string{"target"}},
		{"same-start-fallback", 0x1000, false, []uint64{0x1009, 0x1020}, []string{"target", "dynamic"}},
		{"overlap-fallback", 0x1008, false, []uint64{0x1009, 0x1020}, []string{"target", "dynamic"}},
		{"missing-dynsym", 0, true, []uint64{0x1020}, []string{""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			image := readELFFixture(t, "symbols64.elf")
			f := openELFFixture(t, image)
			dynamic := f.SectionByType(elf.SHT_DYNSYM)
			if tc.missingDynamic {
				dynamic.Type = elf.SHT_NULL
			} else {
				binary.LittleEndian.PutUint64(image[dynamic.Offset+elf.Sym64Size+8:], tc.dynamicStart)
			}
			got, err := elfSymbolsForPCsWithState(f, tc.pcs, newELFSymbolParseState(DefaultELFSymbolLimits()))
			if err != nil {
				t.Fatal(err)
			}
			for i, pc := range tc.pcs {
				if got.resolve(pc) != tc.want[i] {
					t.Fatalf("PC %#x: got %q, want %q", pc, got.resolve(pc), tc.want[i])
				}
			}
		})
	}
}

func TestELFSymbolIndexNotCachedOnNameFailure(t *testing.T) {
	image := readELFFixture(t, "symbols64.elf")
	f := openELFFixture(t, image)
	section := f.SectionByType(elf.SHT_SYMTAB)
	binary.LittleEndian.PutUint32(image[section.Offset+elf.Sym64Size:], math.MaxUint32)
	state := newELFSymbolParseState(DefaultELFSymbolLimits())
	if _, err := state.parseSource(f, elfSymbolTable{typ: elf.SHT_SYMTAB}, 0x1001); err == nil {
		t.Fatal("expected invalid name offset")
	}
	if len(state.indexes) != 0 || state.symbolCount != 0 || state.metadataBytes != 0 {
		t.Fatal("failed source retained an index or its metadata reservation")
	}
}

func TestELFNameFailureLeavesBudgetForFallback(t *testing.T) {
	image := readELFFixture(t, "symbols64.elf")
	f := openELFFixture(t, image)
	section := f.SectionByType(elf.SHT_SYMTAB)
	binary.LittleEndian.PutUint32(image[section.Offset+elf.Sym64Size:], math.MaxUint32)
	limits := DefaultELFSymbolLimits()
	limits.MaxMetadataBytes = section.Size
	limits.MaxSymbolAndCacheCount = 2
	state := newELFSymbolParseState(limits)
	got, err := elfSymbolsForPCsWithState(f, []uint64{0x1001}, state)
	if err == nil || got.resolve(0x1001) != "dynamic" {
		t.Fatalf("got %v, %v", got, err)
	}
	if len(state.indexes) != 1 || state.symbolCount != 1 || state.metadataBytes != 2*elf.Sym64Size {
		t.Fatalf("incorrect fallback accounting: indexes=%d symbols=%d bytes=%d", len(state.indexes), state.symbolCount, state.metadataBytes)
	}
}

func TestELFSymbolCountLimit(t *testing.T) {
	f := openELFFixture(t, readELFFixture(t, "symbols64.elf"))
	limits := DefaultELFSymbolLimits()
	limits.MaxSymbolAndCacheCount = 1
	got, err := newELFSymbolParseState(limits).parseSource(f, elfSymbolTable{typ: elf.SHT_SYMTAB}, 0x1001)
	if !errors.Is(err, errELFSymbolLimit) || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestELFSymbol32(t *testing.T) {
	f := openELFFixture(t, readELFFixture(t, "symbols32.elf"))
	got, err := elfSymbolsForPCsWithState(f, []uint64{0x1001}, newELFSymbolParseState(DefaultELFSymbolLimits()))
	if err != nil || got.resolve(0x1001) != "target" {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestELFCompressedBudget(t *testing.T) {
	for _, tc := range []struct {
		name, file string
		shortBy    uint64
	}{
		{"modern-exact", "compressed64.elf", 0},
		{"modern-short", "compressed64.elf", 1},
		{"legacy-exact", "legacy64.elf", 0},
		{"legacy-short", "legacy64.elf", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := openELFFixture(t, readELFFixture(t, tc.file))
			stringsSection := f.Sections[f.SectionByType(elf.SHT_SYMTAB).Link]
			stringsSection.Open()
			limits := DefaultELFSymbolLimits()
			limits.MaxMetadataBytes = f.SectionByType(elf.SHT_SYMTAB).Size + stringsSection.Size - tc.shortBy
			state := newELFSymbolParseState(limits)
			got, err := state.parseSource(f, elfSymbolTable{typ: elf.SHT_SYMTAB}, 0x1001)
			if tc.shortBy != 0 {
				if !errors.Is(err, errELFSymbolLimit) || len(got) != 0 {
					t.Fatalf("got %v, %v", got, err)
				}
				return
			}
			if err != nil || got.resolve(0x1001) != "target" {
				t.Fatalf("got %v, %v", got, err)
			}
			if state.metadataBytes != limits.MaxMetadataBytes {
				t.Fatal("incorrect exact-budget accounting")
			}
			// A new name in a later batch requires a second decompression.
			if _, err := state.parseSource(f, elfSymbolTable{typ: elf.SHT_SYMTAB}, 0x1011); !errors.Is(err, errELFSymbolLimit) {
				t.Fatalf("second decompression: %v", err)
			}
		})
	}
}

func TestELFLegacyCompressedStringOffset(t *testing.T) {
	f := openELFFixture(t, readELFFixture(t, "legacy64.elf"))
	const nameOffset = 4096
	if f.Sections[1].Size >= nameOffset {
		t.Fatal("fixture must place name beyond compressed data")
	}
	got, err := newELFSymbolParseState(DefaultELFSymbolLimits()).parseSource(f, elfSymbolTable{typ: elf.SHT_SYMTAB}, 0x1001)
	if err != nil || got.resolve(0x1001) != "target" {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestELFPCResultCacheIsBoundedAcrossBatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "symbols.elf")
	if err := os.WriteFile(path, readELFFixture(t, "symbols64.elf"), 0o600); err != nil {
		t.Fatal(err)
	}
	limits := DefaultELFSymbolLimits()
	limits.MaxSymbolAndCacheCount = 2
	resolver := NewUsymResolver(WithELFSymbolLimits(limits))
	cache := &elfSymbolCache{}
	for _, pc := range []uint64{0x1001, 0x1002, 0x1003} {
		const miss = 0x10000
		got, err := resolver.resolveELFPCs(path, cache, []uint64{pc, miss})
		// A miss probes dynsym, which cannot fit alongside the cached symtab.
		if !errors.Is(err, errELFSymbolLimit) || got[pc] != "target" || got[miss] != "" {
			t.Fatalf("PC %#x: got %v, %v", pc, got, err)
		}
		if len(cache.namesByELFPC) > 2 {
			t.Fatal("PC cache exceeds cap")
		}
		if _, ok := cache.namesByELFPC[miss]; ok {
			t.Fatal("cache retained a miss")
		}
	}
	if len(cache.namesByELFPC) != 2 {
		t.Fatal("cache did not fill to limit")
	}
}

// The image stays writable so each test can patch only its relevant ELF field.
func readELFFixture(t *testing.T, name string) []byte {
	t.Helper()
	image, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return image
}

func openELFFixture(t *testing.T, image []byte) *elf.File {
	t.Helper()
	f, err := elf.NewFile(bytes.NewReader(image))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
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
