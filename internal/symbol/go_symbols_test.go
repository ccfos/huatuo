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
