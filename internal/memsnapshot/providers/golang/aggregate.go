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
	"cmp"
	"container/heap"
	"encoding/binary"
	"math"
	"slices"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

const (
	memRecordBytes = 4 * 4 * 8
	// Bound stable stack keys retained by the global aggregate map. Map and
	// allocation metadata add overhead beyond this byte budget.
	maxAggregateKeyBytes = 32 << 20
)

type minHeap []allocation

type allocation struct {
	key          string
	inuseBytes   int64
	inuseObjects int64
}

// allocationTotals is kept separately from the aggregate map key so each
// retained stack has only one string header.
type allocationTotals struct {
	inuseBytes   int64
	inuseObjects int64
}

func decodeInUse(raw []byte, order binary.ByteOrder) (uint64, uint64) {
	var allocs, frees, allocBytes, freeBytes uint64
	for base := 0; base < memRecordBytes; base += 32 {
		allocs = memsnapshot.SaturatingAdd(allocs, order.Uint64(raw[base:base+8]))
		frees = memsnapshot.SaturatingAdd(frees, order.Uint64(raw[base+8:base+16]))
		allocBytes = memsnapshot.SaturatingAdd(allocBytes, order.Uint64(raw[base+16:base+24]))
		freeBytes = memsnapshot.SaturatingAdd(freeBytes, order.Uint64(raw[base+24:base+32]))
	}
	return posDelta(allocs, frees),
		posDelta(allocBytes, freeBytes)
}

func posDelta(left, right uint64) uint64 {
	if right > left {
		return 0
	}
	return left - right
}

func stackPCPrefix(stack []byte, order binary.ByteOrder) []byte {
	for offset := 0; offset < len(stack); offset += 8 {
		if order.Uint64(stack[offset:offset+8]) == 0 {
			return stack[:offset]
		}
	}
	return stack
}

func aggregateAllocation(aggregates map[string]int, totals *[]allocationTotals,
	stack []byte, objects, bytes int64, aggregateKeyBytes *int, maxKeyBytes int,
) bool {
	// The []byte-to-string conversion used only for map lookup does not allocate.
	// Copy the stack once only when a new aggregate needs a stable key.
	index, exists := aggregates[string(stack)]
	if !exists {
		if len(stack) > maxKeyBytes-*aggregateKeyBytes {
			return false
		}
		key := string(stack)
		index = len(*totals)
		aggregates[key] = index
		*totals = append(*totals, allocationTotals{})
		*aggregateKeyBytes += len(key)
	}
	aggregate := &(*totals)[index]
	aggregate.inuseObjects = saturatedInt64Add(aggregate.inuseObjects, objects)
	aggregate.inuseBytes = saturatedInt64Add(aggregate.inuseBytes, bytes)
	return true
}

func (h minHeap) Len() int { return len(h) }

func (h minHeap) Less(i, j int) bool {
	if h[i].inuseBytes == h[j].inuseBytes {
		return h[i].key > h[j].key
	}
	return h[i].inuseBytes < h[j].inuseBytes
}

func (h minHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *minHeap) Push(value any) { *h = append(*h, value.(allocation)) }

func (h *minHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

func keepTop(candidates *minHeap, limit int,
	candidate allocation,
) {
	if candidates.Len() < limit {
		heap.Push(candidates, candidate)
		return
	}
	worst := (*candidates)[0]
	if candidate.inuseBytes < worst.inuseBytes ||
		(candidate.inuseBytes == worst.inuseBytes && candidate.key >= worst.key) {
		return
	}
	(*candidates)[0] = candidate
	heap.Fix(candidates, 0)
}

func sortCandidates(candidates minHeap) {
	slices.SortFunc(candidates, func(left, right allocation) int {
		if byBytes := cmp.Compare(right.inuseBytes, left.inuseBytes); byBytes != 0 {
			return byBytes
		}
		return cmp.Compare(left.key, right.key)
	})
}

// scaleHeapSample follows runtime/pprof's Poisson sampling correction.
func scaleHeapSample(count, size, rate int64) (int64, int64) {
	if count == 0 || size == 0 {
		return 0, 0
	}
	if rate <= 1 {
		return count, size
	}
	averageSize := float64(size) / float64(count)
	scale := 1 / (1 - math.Exp(-averageSize/float64(rate)))
	return clampScaleToInt64(float64(count) * scale),
		clampScaleToInt64(float64(size) * scale)
}

// clampScaleToInt64 saturates the scaled sample to int64. A victim configured
// with an extreme runtime.MemProfileRate could otherwise overflow the float64
// -> int64 conversion, which is implementation-defined and yields a negative
// in-use value downstream.
func clampScaleToInt64(value float64) int64 {
	if value >= math.MaxInt64 {
		return math.MaxInt64
	}
	if value <= 0 {
		return 0
	}
	return int64(value)
}

func clampUint64(value uint64) int64 {
	if value > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(value)
}

func saturatedInt64Add(left, right int64) int64 {
	if right > 0 && left > math.MaxInt64-right {
		return math.MaxInt64
	}
	return left + right
}
