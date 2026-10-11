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
	"encoding/binary"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

func buildEntries(ctx context.Context,
	allocations []allocation, order binary.ByteOrder, symbols *symbolizer,
) ([]memsnapshot.Entry, error) {
	entries := make([]memsnapshot.Entry, 0, len(allocations))
	for _, candidate := range allocations {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		name, stack := symbols.resolveStack([]byte(candidate.key), order)
		average := float64(0)
		if candidate.inuseObjects != 0 {
			average = float64(candidate.inuseBytes) / float64(candidate.inuseObjects)
		}
		entries = append(entries, memsnapshot.Entry{
			Kind: "inuse_space_objects", Name: name,
			Bytes: uint64(candidate.inuseBytes), Objects: uint64(candidate.inuseObjects), AverageBytes: average, Stack: stack,
		})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return entries, nil
}
