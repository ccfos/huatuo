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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestDiscoverPIDRejectsPIESymbolOverflow(t *testing.T) {
	workspace := t.TempDir()
	source := filepath.Join(workspace, "main.go")
	if err := os.WriteFile(source, []byte("package main\nimport \"runtime\"\nfunc main() { runtime.GC() }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(workspace, "fixture")
	if output, err := exec.Command("go", "build", "-buildmode=pie", "-o", executable, source).CombinedOutput(); err != nil {
		t.Fatalf("build PIE fixture: %v: %s", err, output)
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
	symbols, err := file.Symbols()
	if err != nil {
		t.Fatal(err)
	}
	valueOffsets := make(map[string]uint64)
	for index, symbol := range symbols {
		if symbol.Name == "runtime.mbuckets" || symbol.Name == "runtime.MemProfileRate" {
			// debug/elf omits the null symbol at index zero.
			valueOffsets[symbol.Name] = file.Section(".symtab").Offset + uint64(index+1)*24 + 8
		}
	}
	if len(valueOffsets) != 2 {
		t.Fatalf("missing runtime symbols: %v", valueOffsets)
	}
	loadOffset, loadVaddr, err := firstLoadSegment(file)
	if err != nil {
		t.Fatal(err)
	}
	stat, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		t.Fatal(err)
	}
	const bias = uint64(0x10000)
	for _, test := range []struct {
		name, symbol string
		value        uint64
		wantError    bool
	}{
		{"ordinary PIE", "runtime.mbuckets", 0x1000, false},
		{"mbuckets wraps to low address", "runtime.mbuckets", ^uint64(0) - bias + 0x1001, true},
		{"rate wraps to low address", "runtime.MemProfileRate", ^uint64(0) - bias + 0x1001, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			procRoot := t.TempDir()
			pidDir := filepath.Join(procRoot, "123")
			if err := os.Mkdir(pidDir, 0o700); err != nil {
				t.Fatal(err)
			}
			data := bytes.Clone(raw)
			file.ByteOrder.PutUint64(data[valueOffsets[test.symbol]:], test.value)
			for name, contents := range map[string][]byte{"exe": data, "stat": stat} {
				if err := os.WriteFile(filepath.Join(pidDir, name), contents, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			exeStat, err := os.Stat(filepath.Join(pidDir, "exe"))
			if err != nil {
				t.Fatal(err)
			}
			maps := fmt.Sprintf("%x-%x r-xp %08x 00:00 %d /fixture\n",
				loadVaddr+bias, loadVaddr+bias+0x1000, loadOffset, exeStat.Sys().(*syscall.Stat_t).Ino)
			if err := os.WriteFile(filepath.Join(pidDir, "maps"), []byte(maps), 0o600); err != nil {
				t.Fatal(err)
			}
			target, err := discoverPID(context.Background(), procRoot, 123)
			if test.wantError {
				if err == nil || !strings.Contains(err.Error(), test.symbol+" address overflows") {
					t.Fatalf("error = %v, want %s address overflow", err, test.symbol)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, want := target.mbuckets(), test.value+bias
			if got != want {
				t.Fatalf("resolved address = %#x, want %#x", got, want)
			}
		})
	}
}
