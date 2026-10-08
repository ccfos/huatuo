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

package memsnapshot

import (
	"bytes"
	"debug/elf"
	"os"
	"path/filepath"
	"testing"
)

func TestDetectLanguageELF(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	file, err := elf.NewFile(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if file.Class != elf.ELFCLASS64 {
		t.Skip("ELF64 fixture required")
	}
	// Preserve all existing sections and append valid null sections. This is a
	// valid Go executable even when its section count exceeds the former policy.
	large := bytes.Clone(raw)
	order := file.ByteOrder
	offset := order.Uint64(raw[40:48])
	size := order.Uint16(raw[58:60])
	count := order.Uint16(raw[60:62])
	const extendedCount = 4097
	if count == 0 || count >= extendedCount {
		t.Fatal("unexpected fixture section count")
	}
	headers := make([]byte, extendedCount*int(size))
	copy(headers, raw[offset:offset+uint64(count)*uint64(size)])
	order.PutUint64(large[40:48], uint64(len(large)))
	order.PutUint16(large[60:62], extendedCount)
	large = append(large, headers...)
	for _, test := range []struct {
		name      string
		data      []byte
		wantError bool
	}{
		{"ordinary Go executable", raw, false},
		{"many sections", large, false},
		{"invalid magic", []byte("not ELF"), true},
		{"truncated header", raw[:20], true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "exe")
			if err := os.WriteFile(path, test.data, 0o600); err != nil {
				t.Fatal(err)
			}
			language, err := detectLanguage(path, "")
			if test.wantError {
				if err == nil || language != LanguageUnknown {
					t.Fatalf("language=%s error=%v", language, err)
				}
				return
			}
			if err != nil || language != LanguageGo {
				t.Fatalf("language=%s error=%v", language, err)
			}
		})
	}
}

func TestLanguageSymbolErrorsBoundSectionNames(t *testing.T) {
	for _, stringsTable := range []bool{false, true} {
		section := &elf.Section{SectionHeader: elf.SectionHeader{Type: elf.SHT_SYMTAB, Entsize: 24, Link: 1}}
		names := &elf.Section{SectionHeader: elf.SectionHeader{Type: elf.SHT_STRTAB}}
		if stringsTable {
			names.Name = ".zdebug" + string(bytes.Repeat([]byte("x"), 1<<20))
		} else {
			section.Name = ".zdebug" + string(bytes.Repeat([]byte("x"), 1<<20))
		}
		_, err := languageFromSymbols(&elf.File{FileHeader: elf.FileHeader{Class: elf.ELFCLASS64}, Sections: []*elf.Section{section, names}})
		if err == nil || len(err.Error()) > 100 {
			t.Fatalf("unbounded parser error: %v", err)
		}
	}
}
