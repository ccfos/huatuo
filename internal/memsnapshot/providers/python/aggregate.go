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

package python

import (
	"cmp"
	"container/heap"
	"slices"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

// The root is the lowest ranked retained type; the full census is aggregated
// before selection so TopK never changes which objects contribute to ranking.
type objectHeap []*memsnapshot.ObjectAggregate

func (h objectHeap) Len() int           { return len(h) }
func (h objectHeap) Less(i, j int) bool { return compareObjects(h[i], h[j]) > 0 }
func (h objectHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *objectHeap) Push(value any)    { *h = append(*h, value.(*memsnapshot.ObjectAggregate)) }
func (h *objectHeap) Pop() any {
	last := len(*h) - 1
	value := (*h)[last]
	(*h)[last] = nil
	*h = (*h)[:last]
	return value
}

func compareObjects(left, right *memsnapshot.ObjectAggregate) int {
	if order := cmp.Compare(right.ShallowBytes, left.ShallowBytes); order != 0 {
		return order
	}
	if order := cmp.Compare(left.TypeName, right.TypeName); order != 0 {
		return order
	}
	return cmp.Compare(right.Count, left.Count)
}

func (c *scanner) entries(topK int) []memsnapshot.Entry {
	limit := min(topK, len(c.aggregates))
	if limit == 0 {
		return nil
	}
	selected := make(objectHeap, 0, limit)
	for _, aggregate := range c.aggregates {
		if len(selected) < limit {
			heap.Push(&selected, aggregate)
		} else if compareObjects(aggregate, selected[0]) < 0 {
			selected[0] = aggregate
			heap.Fix(&selected, 0)
		}
	}
	slices.SortFunc(selected, compareObjects)
	result := make([]memsnapshot.Entry, 0, len(selected))
	for _, aggregate := range selected {
		var average float64
		if aggregate.Count != 0 {
			average = float64(aggregate.ShallowBytes) / float64(aggregate.Count)
		}
		result = append(result, memsnapshot.Entry{
			Kind: "gc_tracked_object_type", Name: aggregate.TypeName,
			Bytes: aggregate.ShallowBytes, Objects: aggregate.Count, AverageBytes: average,
		})
	}
	return result
}
