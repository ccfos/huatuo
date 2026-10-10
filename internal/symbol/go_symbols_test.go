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
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestReadGoTable(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "main.go")
	if err := os.WriteFile(source, []byte("package main\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"exe", "pie", "stripped"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(dir, mode)
			args := []string{"build", "-o", path}
			if mode == "pie" {
				args = append(args, "-buildmode=pie")
			}
			if mode == "stripped" {
				args = append(args, "-ldflags=-s -w")
			}
			args = append(args, source)
			if out, err := exec.Command("go", args...).CombinedOutput(); err != nil {
				t.Fatalf("build: %v: %s", err, out)
			}
			file, err := elf.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			table, err := ReadGoTable(t.Context(), file)
			if err != nil {
				t.Fatal(err)
			}
			fn := table.LookupFunc("main.main")
			if fn == nil || table.PCToFunc(fn.Entry) != fn {
				t.Fatal("missing main.main at link-time PC")
			}
			if _, err := file.Section(".text").Data(); err != nil {
				t.Fatalf("caller file closed: %v", err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err := ReadGoTable(ctx, file); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel: %v", err)
			}
			if mode != "exe" {
				return
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			section := file.Section(".gopclntab")
			for _, test := range []struct {
				name  string
				patch func([]byte)
			}{
				{"magic", func(p []byte) { p[0] = 0 }},
				{"count", func(p []byte) { file.ByteOrder.PutUint64(p[8:], maxGoSymbolEntries+1) }},
				{"offset", func(p []byte) { file.ByteOrder.PutUint64(p[32:], uint64(len(raw))) }},
			} {
				t.Run(test.name, func(t *testing.T) {
					data := bytes.Clone(raw)
					test.patch(data[section.Offset:])
					corrupt, err := elf.NewFile(bytes.NewReader(data))
					if err != nil {
						t.Fatal(err)
					}
					defer corrupt.Close()
					if _, err := ReadGoTable(t.Context(), corrupt); err == nil {
						t.Fatal("accepted corrupt pclntab")
					}
				})
			}
			section.Size = maxGoSymbolBytes
			if _, err := ReadGoTable(t.Context(), file); err == nil {
				t.Fatal("accepted oversized pclntab")
			}
		})
	}
}

func TestGoSymbolBudget(t *testing.T) {
	for _, counts := range [][3]uint64{{maxGoSymbolBytes, 0, 0}, {0, ^uint64(0), 0}, {0, 0, ^uint64(0)}} {
		if _, err := goSymbolBudget(counts[0], counts[1], counts[2]); err == nil {
			t.Fatalf("accepted budget: %v", counts)
		}
	}
}

func TestValidatePCLNFileReferences(t *testing.T) {
	// A valid minimal Go 1.20 table with a trailing undeclared filename.
	makeTable := func() []byte {
		p := make([]byte, 168)
		o := binary.LittleEndian
		o.PutUint32(p, 0xfffffff1)
		p[6], p[7] = 1, 8
		for i, v := range []uint64{1, 1, 0x1000, 72, 80, 88, 104, 112} {
			o.PutUint64(p[8+i*8:], v)
		}
		copy(p[72:], "main.f\x00")
		o.PutUint32(p[80:], 0)
		o.PutUint32(p[84:], ^uint32(0))
		copy(p[88:], "a.go\x00hidden.go\x00")
		// offset 1: file 0, PC advance 1, end; offset 4: line 1.
		copy(p[104:], []byte{0, 2, 1, 0, 4, 1, 0})
		o.PutUint32(p[116:], 12)
		o.PutUint32(p[120:], 1)
		o.PutUint32(p[124+20:], 1)
		o.PutUint32(p[124+24:], 4)
		return p
	}
	for _, test := range []struct {
		name  string
		patch func([]byte)
	}{
		{"valid", func([]byte) {}},
		{"interior_filename", func(p []byte) { binary.LittleEndian.PutUint32(p[80:], 1) }},
		{"undeclared_filename", func(p []byte) { binary.LittleEndian.PutUint32(p[80:], 5) }},
		{"out_of_bounds_filename", func(p []byte) { binary.LittleEndian.PutUint32(p[80:], ^uint32(0)-1) }},
		{"unit_index", func(p []byte) { binary.LittleEndian.PutUint32(p[124+32:], ^uint32(0)) }},
		{"file_number", func(p []byte) { p[105] = 8 }},
		{"pcfile_offset", func(p []byte) { binary.LittleEndian.PutUint32(p[124+20:], 99) }},
		{"line_offset", func(p []byte) { binary.LittleEndian.PutUint32(p[124+24:], 99) }},

		{"unterminated_varint", func(p []byte) { copy(p[105:], []byte{255, 255, 255, 255, 255}) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := makeTable()
			test.patch(p)
			err := validatePCLN(t.Context(), p, binary.LittleEndian)
			if (err == nil) != (test.name == "valid") {
				t.Fatalf("validation = %v", err)
			}
		})
	}
	t.Run("oversized_undeclared_filename", func(t *testing.T) {
		original := makeTable()
		filenames := append([]byte("a.go\x00"), bytes.Repeat([]byte("x"), maxGoSymbolName+1)...)
		filenames = append(filenames, 0)
		p := append(append(bytes.Clone(original[:88]), filenames...), original[104:]...)
		binary.LittleEndian.PutUint64(p[8+6*8:], uint64(88+len(filenames)))
		binary.LittleEndian.PutUint64(p[8+7*8:], uint64(96+len(filenames)))
		binary.LittleEndian.PutUint32(p[80:], 5)
		if err := validatePCLN(t.Context(), p, binary.LittleEndian); err == nil {
			t.Fatal("accepted reachable oversized undeclared filename")
		}
	})

	p := makeTable()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := validatePCLN(ctx, p, binary.LittleEndian); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}

func TestValidateGoPCValuesBudgets(t *testing.T) {
	for _, size := range []int{4, 64 << 10} {
		data := append([]byte{0}, bytes.Repeat([]byte{2, 1}, size)...)
		data = append(data, 0)
		budget := size
		if size > 4 {
			budget = maxGoSymbolBytes
		}
		if _, err := validateGoPCValues(t.Context(), data, 1, 1, &budget); err == nil {
			t.Fatal("accepted excessive PC-value decoding")
		}
	}
}
