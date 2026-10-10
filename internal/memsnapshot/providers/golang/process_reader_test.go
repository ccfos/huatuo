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
	"context"
	"debug/elf"
	"errors"
	"os"
	"testing"
)

// Construct the reader at the file/memory boundary without a synthetic procfs.
func syntheticReader(t *testing.T) (*processReader, []byte) {
	t.Helper()
	memory, info, heap := bucketFixture(t, 2)
	executable := runtimeExecutable(t, false)
	file, err := elf.NewFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	reader := &processReader{executable: executable, elfFile: file, runtime: info, memory: *memory}
	t.Cleanup(func() { _ = reader.Close() })
	return reader, heap
}

func TestProcessReaderRetainsExecutable(t *testing.T) {
	reader, _ := syntheticReader(t)
	path := reader.executable.Name()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	symbols, err := reader.buildSymbolizer(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	fn := symbols.table.LookupFunc("runtime.GC")
	if fn == nil {
		t.Fatal("runtime.GC is missing from pinned executable")
	}
	remaining := maxGoFrameBytes
	if name, _, err := symbols.resolve(t.Context(), fn.Entry+1, &remaining); err != nil || name != "runtime.GC" {
		t.Fatal("cannot resolve pinned executable")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := reader.buildSymbolizer(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.executable.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("file not closed: %v", err)
	}
}
