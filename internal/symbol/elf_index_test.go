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
	"compress/zlib"
	"debug/elf"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"testing"
)

// Reuse the small ELF fixture's headers and string table, replacing only symtab.
func indexFixture(t testing.TB, data []byte, compression string) ([]byte, uint64) {
	t.Helper()
	image, err := os.ReadFile("testdata/symbols64.elf")
	if err != nil {
		t.Fatal(err)
	}
	f, err := elf.NewFile(bytes.NewReader(image))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for i, s := range f.Sections {
		if s.Type != elf.SHT_SYMTAB {
			continue
		}
		header := binary.LittleEndian.Uint64(image[40:48]) + uint64(i)*uint64(binary.LittleEndian.Uint16(image[58:60]))
		if compression != "" {
			var output bytes.Buffer
			w := zlib.NewWriter(&output)
			if _, err = w.Write(data); err != nil {
				t.Fatal(err)
			}
			if err = w.Close(); err != nil {
				t.Fatal(err)
			}
			var ch []byte
			if compression == "legacy" {
				ch = make([]byte, 12)
				copy(ch, "ZLIB")
				binary.BigEndian.PutUint64(ch[4:], uint64(len(data)))
			} else {
				ch = make([]byte, 24)
				binary.LittleEndian.PutUint32(ch, uint32(elf.COMPRESS_ZLIB))
				binary.LittleEndian.PutUint64(ch[8:], uint64(len(data)))
				binary.LittleEndian.PutUint64(ch[16:], 8)
				binary.LittleEndian.PutUint64(image[header+8:], uint64(elf.SHF_COMPRESSED))
			}
			ch = append(ch, output.Bytes()...)
			data = ch
		}
		offset := uint64(len(image))
		binary.LittleEndian.PutUint64(image[header+24:], offset)
		binary.LittleEndian.PutUint64(image[header+32:], uint64(len(data)))
		return append(image, data...), offset
	}
	t.Fatal("missing symtab fixture")
	return nil, 0
}

func indexRecords(class elf.Class, order binary.ByteOrder, count, stride int) []byte {
	size := elf.Sym64Size
	if class == elf.ELFCLASS32 {
		size = elf.Sym32Size
	}
	data := make([]byte, (count+1)*size)
	for i := 1; i <= count; i++ {
		entry := data[i*size:]
		order.PutUint32(entry, uint32(i))
		info := byte(elf.STT_OBJECT)
		if stride > 0 && i%stride == 0 {
			info = byte(elf.STT_FUNC)
		}
		// Descending duplicate addresses make stable ordering observable.
		addr := uint64(0x1000 + (count-i)/2*16)
		if class == elf.ELFCLASS32 {
			order.PutUint32(entry[4:], uint32(addr))
			order.PutUint32(entry[8:], 8)
			entry[12] = info
		} else {
			entry[4] = info
			order.PutUint64(entry[8:], addr)
			order.PutUint64(entry[16:], 8)
		}
	}
	return data
}

func TestELFIndexChunkBoundaries(t *testing.T) {
	for _, class := range []elf.Class{elf.ELFCLASS32, elf.ELFCLASS64} {
		for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
			t.Run(fmt.Sprintf("%s/%s", class, order), func(t *testing.T) {
				size := uint64(elf.Sym64Size)
				if class == elf.ELFCLASS32 {
					size = elf.Sym32Size
				}
				for _, count := range []int{0, 1, int(24*1024/size) - 1, int(24 * 1024 / size), int(48*1024/size) + 3} {
					data := indexRecords(class, order, count, 1)
					image, _ := indexFixture(t, data, "")
					f, err := elf.NewFile(bytes.NewReader(image))
					if err != nil {
						t.Fatal(err)
					}
					defer f.Close()
					f.Class = class
					f.ByteOrder = order
					got, err := buildELFSymbolIndex(f, f.SectionByType(elf.SHT_SYMTAB), size)
					if err != nil {
						t.Fatal(err)
					}
					want := make([]elfSymbolCandidate, 0, count)
					for i := 1; i <= count; i++ {
						want = append(want, elfSymbolCandidate{nameOffset: uint32(i), value: uint64(0x1000 + (count-i)/2*16), size: 8})
					}
					slices.SortStableFunc(want, func(a, b elfSymbolCandidate) int {
						if a.value < b.value {
							return -1
						}
						if a.value > b.value {
							return 1
						}
						return 0
					})
					if !slices.Equal(got, want) {
						t.Fatalf("count %d: wrong index or alias order", count)
					}
					if count > 1 {
						pc := want[0].value
						selected := selectELFSymbolCandidates(got, []uint64{pc})
						last := 0
						for last+1 < len(want) && want[last+1].value == pc {
							last++
						}
						if len(selected) != 1 || selected[0] != want[last] {
							t.Fatal("last equal-address alias was not preserved")
						}
					}
				}
			})
		}
	}
}

func TestELFIndexSparseTables(t *testing.T) {
	for _, stride := range []int{0, 1, 4096} {
		image, _ := indexFixture(t, indexRecords(elf.ELFCLASS64, binary.LittleEndian, 8192, stride), "")
		f := openELFFixture(t, image)
		got, err := buildELFSymbolIndex(f, f.SectionByType(elf.SHT_SYMTAB), elf.Sym64Size)
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		if stride > 0 {
			want = 8192 / stride
		}
		if len(got) != want {
			t.Fatalf("stride %d: got %d functions, want %d", stride, len(got), want)
		}
		if cap(got) > max(1, len(got))*2 {
			t.Fatalf("stride %d retained %d entries for %d functions", stride, cap(got), len(got))
		}
	}
}

type indexReadProbe struct {
	data           []byte
	start          uint64
	bytes, maxRead int
	truncate       bool
}

func (p *indexReadProbe) ReadAt(b []byte, offset int64) (int, error) {
	if uint64(offset) >= p.start {
		p.bytes += len(b)
		p.maxRead = max(p.maxRead, len(b))
		if p.truncate && uint64(offset)+uint64(len(b)) >= uint64(len(p.data)) {
			return 0, io.ErrUnexpectedEOF
		}
	}
	return bytes.NewReader(p.data).ReadAt(b, offset)
}

func TestELFIndexReadFailureAndLimits(t *testing.T) {
	for _, tc := range []struct {
		name     string
		limit    bool
		truncate bool
	}{{"chunks", false, false}, {"budget", true, false}, {"truncated", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			data := indexRecords(elf.ELFCLASS64, binary.LittleEndian, 3000, 1)
			// Every function shares the valid fixture string offset.
			for i := elf.Sym64Size; i < len(data); i += elf.Sym64Size {
				binary.LittleEndian.PutUint32(data[i:], 1)
			}
			image, offset := indexFixture(t, data, "")
			probe := &indexReadProbe{data: image, start: offset, truncate: tc.truncate}
			f, err := elf.NewFile(probe)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			probe.bytes = 0
			probe.maxRead = 0
			limits := DefaultELFSymbolLimits()
			if tc.limit {
				limits.MaxMetadataBytes = uint64(len(data) - 1)
			}
			state := newELFSymbolParseState(limits)
			got, err := state.parseSource(f, elfSymbolTable{typ: elf.SHT_SYMTAB}, 0x1001)
			if tc.limit || tc.truncate {
				if err == nil || len(got) != 0 || len(state.indexes) != 0 || state.symbolCount != 0 || state.metadataBytes != 0 {
					t.Fatalf("failed read published state: %v", err)
				}
				if tc.limit && (!errors.Is(err, errELFSymbolLimit) || probe.bytes != 0) {
					t.Fatalf("budget read %d bytes: %v", probe.bytes, err)
				}
				if tc.truncate && !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatalf("lost read error: %v", err)
				}
				return
			}
			if err != nil || got.resolve(0x1001) != "target" {
				t.Fatalf("resolution: %v %v", got, err)
			}
			if probe.maxRead > 24*1024 {
				t.Fatalf("whole-table read: %d bytes", probe.maxRead)
			}
			reads := probe.bytes
			if _, err = state.parseSource(f, elfSymbolTable{typ: elf.SHT_SYMTAB}, 0x1002); err != nil || probe.bytes != reads {
				t.Fatalf("cached index reread table: %v", err)
			}
		})
	}
}

func TestELFIndexCompressedSymbols(t *testing.T) {
	for _, compression := range []string{"zlib", "legacy"} {
		t.Run(compression, func(t *testing.T) {
			data := indexRecords(elf.ELFCLASS64, binary.LittleEndian, 2050, 1)
			for i := elf.Sym64Size; i < len(data); i += elf.Sym64Size {
				binary.LittleEndian.PutUint32(data[i:], 1)
			}
			image, _ := indexFixture(t, data, compression)
			f := openELFFixture(t, image)
			if compression == "legacy" {
				f.SectionByType(elf.SHT_SYMTAB).Name = ".zdebug_symbols"
			}
			limits := DefaultELFSymbolLimits()
			limits.MaxMetadataBytes = uint64(len(data))
			limits.MaxSymbolAndCacheCount = 2050
			state := newELFSymbolParseState(limits)
			got, err := state.parseSource(f, elfSymbolTable{typ: elf.SHT_SYMTAB}, 0x1001)
			if err != nil || got.resolve(0x1001) != "target" || state.metadataBytes != uint64(len(data)) {
				t.Fatalf("compressed table: %v %v", got, err)
			}
			limits.MaxMetadataBytes--
			if _, err = newELFSymbolParseState(limits).parseSource(f, elfSymbolTable{typ: elf.SHT_SYMTAB}, 0x1001); !errors.Is(err, errELFSymbolLimit) {
				t.Fatalf("expanded budget: %v", err)
			}
		})
	}
}

func BenchmarkELFSymbolIndex(b *testing.B) {
	for _, stride := range []int{1, 128, 0} {
		b.Run(fmt.Sprintf("function-stride-%d", stride), func(b *testing.B) {
			data := indexRecords(elf.ELFCLASS64, binary.LittleEndian, 400000, stride)
			image, _ := indexFixture(b, data, "")
			f, err := elf.NewFile(bytes.NewReader(image))
			if err != nil {
				b.Fatal(err)
			}
			defer f.Close()
			section := f.SectionByType(elf.SHT_SYMTAB)
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			b.ResetTimer()
			for b.Loop() {
				if _, err = buildELFSymbolIndex(f, section, elf.Sym64Size); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
