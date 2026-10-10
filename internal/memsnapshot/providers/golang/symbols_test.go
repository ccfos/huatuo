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
	"debug/elf"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildSymbolizerStrippedExecutable(t *testing.T) {
	executable := strippedRuntimeExecutable(t)
	file, err := elf.NewFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	reader := &processReader{
		executable: executable,
		elfFile:    file,
		runtime: &runtimeInfo{
			loadBias: 0x1000,
			layout:   runtimeLayout{byteOrder: file.ByteOrder},
		},
	}
	first, err := reader.buildSymbolizer(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := reader.buildSymbolizer(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}

	// Each parser owns copied metadata and must survive the executable closing.
	for _, symbols := range []*symbolizer{first, second} {
		fn := symbols.table.LookupFunc("runtime.MemProfile")
		if fn == nil {
			t.Fatal("runtime.MemProfile is missing from the stripped executable")
		}
		var stack [programCounterBytes]byte
		file.ByteOrder.PutUint64(stack[:], fn.Entry+reader.runtime.loadBias+1)
		remaining := maxGoFrameBytes
		name, resolved, err := symbols.resolveStack(t.Context(), stack[:], file.ByteOrder, &remaining)
		if err != nil {
			t.Fatal(err)
		}
		if name != "runtime.MemProfile" || len(resolved) != 1 ||
			!strings.HasPrefix(resolved[0], "runtime.MemProfile, ") ||
			!strings.Contains(resolved[0], "/runtime/mprof.go:") {
			t.Fatalf("stripped stack after closing executable = %q, %v", name, resolved)
		}
	}
	if symbols, err := reader.buildSymbolizer(t.Context()); symbols != nil || err == nil {
		t.Fatalf("closed executable construction = %v, %v; want no symbolizer and a read error", symbols, err)
	}
}

func TestBuildSymbolizerSourceLocations(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "source, files")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(directory, "main.go")
	code := `package main
import "runtime"
//go:noinline
func allocate() []byte {
    return make([]byte, 4096)
}
func main() { runtime.KeepAlive(allocate()) }
`
	if err := os.WriteFile(source, []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"exe", "pie", "stripped"} {
		t.Run(mode, func(t *testing.T) {
			executable := filepath.Join(directory, mode)
			args := []string{"build", "-o", executable}
			if mode == "pie" {
				args = append(args, "-buildmode=pie")
			}
			if mode == "stripped" {
				args = append(args, "-ldflags=-s -w")
			}
			args = append(args, source)
			if output, err := exec.CommandContext(t.Context(), "go", args...).CombinedOutput(); err != nil {
				t.Fatalf("build source-location fixture: %v: %s", err, output)
			}
			file, err := elf.Open(executable)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = file.Close() })
			reader := &processReader{elfFile: file, runtime: &runtimeInfo{loadBias: 0x100000}}
			symbols, err := reader.buildSymbolizer(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			// Query the allocation statement, not the function's declaration line.
			pc, _, err := symbols.table.LineToPC(source, 5)
			if err != nil {
				t.Fatal(err)
			}
			var stack [programCounterBytes]byte
			file.ByteOrder.PutUint64(stack[:], pc+reader.runtime.loadBias+1)
			remaining := maxGoFrameBytes
			name, frames, err := symbols.resolveStack(t.Context(), stack[:], file.ByteOrder, &remaining)
			if err != nil {
				t.Fatal(err)
			}
			wantFrame := fmt.Sprintf("main.allocate, %s:5", source)
			if name != "main.allocate" || len(frames) != 1 || frames[0] != wantFrame {
				t.Fatalf("source location = %q, %v; want main.allocate, [%q]", name, frames, wantFrame)
			}
		})
	}
}
