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

package main

import (
	"container/heap"
)

// IODataStat represents the statistics of an IO operation,
// containing a pointer to the IOBpfData and the IO size in bytes.
type IODataStat struct {
	Data   *IOData
	IOSize uint64
}

// PriorityQueue is a slice of pointers to IODataStat structures
// that implements the heap.Interface to maintain a max-heap of IODataStat items.
type PriorityQueue []*IODataStat

// Len returns the number of elements in the priority queue.
func (pq PriorityQueue) Len() int { return len(pq) }

// Less reports whether the element with index i should sort before the element with index j.
// In this case, it sorts the elements by IOSize in descending order, so the highest IOSize is at the front.
func (pq PriorityQueue) Less(i, j int) bool {
	// We want Pop to give us the highest IOSize
	return pq[i].IOSize > pq[j].IOSize
}

// Swap swaps the elements with indexes i and j.
func (pq PriorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
}

// Push adds an element to the priority queue.
func (pq *PriorityQueue) Push(x any) {
	item := x.(*IODataStat)
	*pq = append(*pq, item)
}

// Pop removes and returns the maximum element (according to Less) from the priority queue.
func (pq *PriorityQueue) Pop() any {
	old := *pq
	n := len(old)
	item := old[n-1]
	*pq = old[0 : n-1]
	return item
}

type FileTable struct {
	data map[uint32]*PriorityQueue
}

func NewFileTable() *FileTable {
	return &FileTable{
		data: make(map[uint32]*PriorityQueue),
	}
}

func (f *FileTable) Update(key uint32, item *IODataStat) {
	if _, ok := f.data[key]; !ok {
		f.data[key] = &PriorityQueue{}
	}

	heap.Push(f.data[key], item)
}

func (f *FileTable) QueueByKey(key uint32) *PriorityQueue {
	return f.data[key]
}
