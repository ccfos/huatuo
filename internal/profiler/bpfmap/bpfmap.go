// Copyright 2025 The HuaTuo Authors
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

// Package bpfmap holds shared BPF-map helpers reused by native profilers
// (cpu/mem) for the A/B double-buffered ring transfer protocol.
package bpfmap

import (
	"bytes"
	"encoding/binary"
	"errors"

	"github.com/cilium/ebpf"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/log"
)

// Indices into profiler_state_map. The BPF program owns the layout; userspace
// must use the same indices to flip parity and read the per-ring sample count.
const (
	TransferCountIdx uint32 = 0
	SampleCountAIdx  uint32 = 1
	SampleCountBIdx  uint32 = 2
)

// StackTraceID pairs a kernel and user stack-map ID emitted with each event.
type StackTraceID struct {
	KernelID int32
	UserID   int32
}

// StackTraceLen matches PERF_MAX_STACK_DEPTH used when allocating BPF stack maps.
const StackTraceLen = 127

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

// BatchReadStackTraces reads the stack arrays for the given IDs from a BPF
// stack map. IDs whose lookup fails or whose value is malformed are silently
// dropped from the result.
func BatchReadStackTraces(b bpf.BPF, mapID uint32, ids map[int32]bool) map[int32][StackTraceLen]uint64 {
	results := make(map[int32][StackTraceLen]uint64, len(ids))
	keyBuf := make([]byte, 4)

	for stackID := range ids {
		binary.LittleEndian.PutUint32(keyBuf, uint32(stackID))

		valBytes, err := b.ReadMap(mapID, keyBuf)
		if err != nil && !errors.Is(err, ebpf.ErrKeyNotExist) {
			log.Warnf("stack map lookup error for ID %d: %v", stackID, err)
			continue
		}

		if err == nil && len(valBytes) == StackTraceLen*8 {
			var trace [StackTraceLen]uint64

			reader := bytes.NewReader(valBytes)
			if err := binary.Read(reader, binary.LittleEndian, &trace); err == nil {
				results[stackID] = trace
			}
		}
	}

	return results
}
