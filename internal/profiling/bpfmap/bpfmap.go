// Copyright 2025, 2026 The HuaTuo Authors
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

// Package bpfmap provides shared stack-map access and native profiler transfer state helpers.
package bpfmap

import (
	"encoding/binary"
	"errors"

	"github.com/cilium/ebpf"

	"github.com/ccfos/huatuo/internal/bpf"
)

// Indices into profiler_state_map. The BPF program owns the layout; userspace
// must use the same indices to flip parity and read the per-ring sample count.
const (
	TransferCountIdx uint32 = 0
	SampleCountAIdx  uint32 = 1
	SampleCountBIdx  uint32 = 2
)

// ReadUint64 fetches a uint64 cell from a BPF map keyed by a uint32 index.
// A missing key is treated as zero so callers can use it on first access.
func ReadUint64(b bpf.BPF, mapID, idx uint32) (uint64, error) {
	var key [4]byte
	binary.LittleEndian.PutUint32(key[:], idx)

	val, err := b.ReadMap(mapID, key[:])
	if err != nil {
		if errors.Is(err, ebpf.ErrKeyNotExist) {
			return 0, nil
		}

		return 0, err
	}

	if len(val) < 8 {
		return 0, nil
	}

	return binary.LittleEndian.Uint64(val), nil
}

// WriteUint64 stores a uint64 value at a uint32-indexed cell in a BPF map.
func WriteUint64(b bpf.BPF, mapID, idx uint32, v uint64) error {
	var key [4]byte
	binary.LittleEndian.PutUint32(key[:], idx)
	var val [8]byte
	binary.LittleEndian.PutUint64(val[:], v)

	return b.WriteMapItems(mapID, []bpf.MapItem{{Key: key[:], Value: val[:]}})
}
