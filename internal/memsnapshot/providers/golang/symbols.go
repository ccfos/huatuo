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
	"debug/gosym"
	"encoding/binary"
	"fmt"
	"os"

	"github.com/ccfos/huatuo/internal/symbol"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

// symbolizer resolves Go PCs from pclntab and remains usable after the
// profiled process exits.
type symbolizer struct {
	table    *gosym.Table
	loadBias uint64
}

// newSymbolizer loads Go symbol metadata from an executable. loadBias is
// subtracted from runtime PCs for PIE binaries.
func newSymbolizer(ctx context.Context, executable string, loadBias uint64,
	table *gosym.Table,
) (*symbolizer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if table != nil {
		return &symbolizer{table: table, loadBias: loadBias}, nil
	}
	executableFile, err := os.Open(executable)
	if err != nil {
		return nil, fmt.Errorf("open executable %q: %w", executable, err)
	}
	defer executableFile.Close()
	file, err := memsnapshot.ReadELFMetadata(ctx, executableFile)
	if err != nil {
		return nil, fmt.Errorf("read executable ELF %q: %w", executable, err)
	}
	defer file.Close()

	table, err = symbol.ReadGoTable(ctx, file)
	if err != nil {
		return nil, fmt.Errorf("parse Go symbol table from %q: %w", executable, err)
	}
	return &symbolizer{table: table, loadBias: loadBias}, nil
}

func (s *symbolizer) resolve(runtimePC uint64) string {
	if s == nil || s.table == nil || runtimePC <= s.loadBias {
		return ""
	}
	// runtime.MemProfile stacks contain return PCs. Move into the call
	// instruction so boundary PCs are attributed to the allocating function.
	function := s.table.PCToFunc(runtimePC - s.loadBias - 1)
	if function == nil {
		return ""
	}
	return function.Name
}

func resolveStack(stack []byte, order binary.ByteOrder,
	symbolizer *symbolizer,
) []string {
	resolved := make([]string, 0, len(stack)/8)
	for offset := 0; offset < len(stack); offset += 8 {
		pc := order.Uint64(stack[offset : offset+8])
		if pc == 0 {
			break
		}
		name := symbolizer.resolve(pc)
		if name == "" {
			name = fmt.Sprintf("0x%x", pc)
		}
		resolved = append(resolved, name)
	}
	return resolved
}
