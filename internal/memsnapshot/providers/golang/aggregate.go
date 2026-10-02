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
	"context"
	"math"
	"slices"
)

// Bound stable stack keys retained by the global aggregate map. Map and
// allocation metadata add overhead beyond this byte budget.
const maxAggregateKeyBytes = 32 << 20

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

// stackAggregates owns stable stack keys and their accumulated sample weights.
type stackAggregates struct {
	indices  map[string]int
	totals   []allocationTotals
	keyBytes int
}

func newStackAggr() *stackAggregates {
	return &stackAggregates{indices: make(map[string]int)}
}

// addSample corrects each sample before merging stacks, because samples sharing
// a stack can have different average allocation sizes. It returns false only
// when retaining a new stack key would exceed the budget.
func (a *stackAggregates) addSample(stack []byte, objects, bytes uint64, sampleRate int64) bool {
	index, exists := a.indices[string(stack)]
	if !exists {
		if len(stack) > maxAggregateKeyBytes-a.keyBytes {
			return false
		}
		// Retained keys must outlive the reusable batch buffer.
		key := string(stack)
		index = len(a.totals)
		a.indices[key] = index
		a.totals = append(a.totals, allocationTotals{})
		a.keyBytes += len(key)
	}

	scaledObjects, scaledBytes := scaleHeapSample(int64(objects), int64(bytes), sampleRate)
	total := &a.totals[index]
	total.inuseObjects += scaledObjects
	total.inuseBytes += scaledBytes
	return true
}

// sortedAllocations returns all aggregates by descending byte count without
// changing the aggregate state. Equal byte counts have no defined order.
func (a *stackAggregates) sortedAllocations(ctx context.Context) ([]allocation, error) {
	allocations := make([]allocation, 0, len(a.indices))
	for key, index := range a.indices {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		total := a.totals[index]
		allocations = append(allocations, allocation{
			key: key, inuseBytes: total.inuseBytes, inuseObjects: total.inuseObjects,
		})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	slices.SortFunc(allocations, func(left, right allocation) int {
		return cmp.Compare(right.inuseBytes, left.inuseBytes)
	})
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return allocations, nil
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

	// Clamp positive estimates before conversion: values >= 2^63, including
	// +Inf, have implementation-dependent int64 results.
	objects, bytes := int64(math.MaxInt64), int64(math.MaxInt64)
	if scaled := float64(count) * scale; scaled < math.MaxInt64 {
		objects = int64(scaled)
	}
	if scaled := float64(size) * scale; scaled < math.MaxInt64 {
		bytes = int64(scaled)
	}
	return objects, bytes
}
