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

package main

import (
	"container/heap"
	"sort"
)

// fileEntry pairs a BPF file-IO record with its sortable size; one
// element of the per-pid file heap.
type fileEntry struct {
	Record *bpfFilesystemIO
	Size   uint64
}

// fileHeap is a max-heap of *fileEntry ordered by Size.
type fileHeap []*fileEntry

func (h fileHeap) Len() int           { return len(h) }
func (h fileHeap) Less(i, j int) bool { return h[i].Size > h[j].Size }
func (h fileHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *fileHeap) Push(x any) {
	*h = append(*h, x.(*fileEntry))
}

func (h *fileHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]

	return item
}

// ProcessIOTable groups IO records by pid: each pid owns a max-heap
// of its per-file stats and a running aggregate used to rank pids.
type ProcessIOTable struct {
	queues map[uint32]*fileHeap
	totals map[uint32]uint64
}

func NewProcessIOTable() *ProcessIOTable {
	return &ProcessIOTable{
		queues: make(map[uint32]*fileHeap),
		totals: make(map[uint32]uint64),
	}
}

// Add pushes entry onto pid's heap and accumulates entry.Size into
// pid's aggregate.
func (t *ProcessIOTable) Add(pid uint32, entry *fileEntry) {
	h, ok := t.queues[pid]
	if !ok {
		h = &fileHeap{}
		t.queues[pid] = h
	}

	heap.Push(h, entry)
	t.totals[pid] += entry.Size
}

// TopN returns at most n pids ordered by descending aggregate IO.
func (t *ProcessIOTable) TopN(n int) []uint32 {
	pids := make([]uint32, 0, len(t.totals))
	for pid := range t.totals {
		pids = append(pids, pid)
	}

	sort.Slice(pids, func(i, j int) bool {
		return t.totals[pids[i]] > t.totals[pids[j]]
	})

	if n < len(pids) {
		pids = pids[:n]
	}

	return pids
}

// Files returns the per-file max-heap for pid, or nil if pid has no
// recorded IO.
func (t *ProcessIOTable) Files(pid uint32) *fileHeap {
	return t.queues[pid]
}
