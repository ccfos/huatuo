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
		if resolved := symbols.resolveStack(stack[:], file.ByteOrder); len(resolved) != 1 || resolved[0] != "runtime.MemProfile" {
			t.Fatalf("stripped stack after closing executable = %v", resolved)
		}
	}
	if symbols, err := reader.buildSymbolizer(t.Context()); symbols != nil || err == nil {
		t.Fatalf("closed executable construction = %v, %v; want no symbolizer and a read error", symbols, err)
	}
}
