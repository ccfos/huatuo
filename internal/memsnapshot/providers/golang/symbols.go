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
	"strings"

	"github.com/ccfos/huatuo/internal/symbol"
)

const maxGoFrameBytes = 8 << 20

// symbolizer only queries copied metadata; it remains usable after the target exits.
type symbolizer struct {
	table    *gosym.Table
	loadBias uint64
}

func (r *processReader) buildSymbolizer(ctx context.Context) (*symbolizer, error) {
	table, err := symbol.ReadGoTable(ctx, r.elfFile)
	if err != nil {
		return nil, err
	}

	return &symbolizer{table: table, loadBias: r.runtime.loadBias}, nil
}

// Keep the function name separate from its display frame so source locations
// cannot change the allocation-site name, including names containing commas.
func (s *symbolizer) resolve(ctx context.Context, runtimePC uint64, remaining *int) (name, frame string, err error) {
	if s == nil || s.table == nil || runtimePC <= s.loadBias {
		return "", "", nil
	}
	// runtime.MemProfile stacks contain return PCs. Move into the call
	// instruction so boundary PCs are attributed to the allocating function.
	file, line, function := s.table.PCToLine(runtimePC - s.loadBias - 1)
	if function == nil {
		return "", "", nil
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	// Charge the entry name, frame string and slice slot before formatting.
	charge := 2*len(function.Name) + 16
	if file != "" && line > 0 {
		charge += len(file) + 32
	}
	if charge > *remaining {
		return "", "", fmt.Errorf("Go snapshot frame-byte budget exceeded")
	}
	*remaining -= charge
	if file == "" || line <= 0 {
		return function.Name, function.Name, nil
	}
	return function.Name, fmt.Sprintf("%s, %s:%d", function.Name, file, line), nil
}

// A nil symbolizer preserves raw PCs so collected allocations remain available.
func (s *symbolizer) resolveStack(ctx context.Context, stack []byte, order binary.ByteOrder, remaining *int) (name string, frames []string, err error) {
	if len(stack)%8 != 0 {
		return "", nil, fmt.Errorf("Go snapshot stack is misaligned")
	}
	if len(stack)/8 > *remaining/16 {
		return "", nil, fmt.Errorf("Go snapshot frame-byte budget exceeded")
	}
	frames = make([]string, 0, len(stack)/8)
	var firstName string
	for offset := 0; offset < len(stack); offset += 8 {
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		pc := order.Uint64(stack[offset : offset+8])
		function, frame, err := s.resolve(ctx, pc, remaining)
		if err != nil {
			return "", nil, err
		}
		if function == "" {
			if *remaining < 52 {
				return "", nil, fmt.Errorf("Go snapshot frame-byte budget exceeded")
			}
			*remaining -= 52
			function = fmt.Sprintf("0x%x", pc)
			frame = function
		}
		if offset == 0 {
			firstName = function
		}
		// Match pprof's allocation-site naming while retaining runtime frames.
		if name == "" && !strings.HasPrefix(function, "runtime.") &&
			!strings.HasPrefix(function, "internal/runtime/") {
			name = function
		}
		frames = append(frames, frame)
	}
	if name == "" {
		name = firstName
	}
	return name, frames, ctx.Err()
}
