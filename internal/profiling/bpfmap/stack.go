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

package bpfmap

import (
	"encoding/binary"
	"fmt"

	"github.com/ccfos/huatuo/internal/bpf"
)

// StackTraceLen matches PERF_MAX_STACK_DEPTH used when allocating BPF stack maps.
const StackTraceLen = 127

// ReadStackTrace reads a stack by ID, including ID zero. Callers must filter
// capture-failure sentinels before reading. Lookup errors retain their identity;
// malformed values wrap ErrInvalidStackData. No partial stack is returned.
func ReadStackTrace(b bpf.BPF, mapID, stackID uint32) ([StackTraceLen]uint64, error) {
	var stack [StackTraceLen]uint64
	var key [4]byte
	binary.LittleEndian.PutUint32(key[:], stackID)
	raw, err := b.ReadMap(mapID, key[:])
	if err != nil {
		return stack, fmt.Errorf("read stack map %d ID %d: %w", mapID, stackID, err)
	}

	if len(raw) != StackTraceLen*8 {
		return stack, fmt.Errorf(
			"%w: map %d ID %d: got %d bytes, want %d",
			ErrInvalidStackData, mapID, stackID, len(raw), StackTraceLen*8,
		)
	}

	for i := range stack {
		stack[i] = binary.LittleEndian.Uint64(raw[i*8:])
	}

	return stack, nil
}
