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

import "sort"

type SortTable struct {
	data map[uint32]uint64
}

func NewSortTable() *SortTable {
	return &SortTable{
		data: make(map[uint32]uint64),
	}
}

func (m *SortTable) Update(key uint32, value uint64) {
	m.data[key] += value
}

func (m *SortTable) TopKeyN(n int) []uint32 {
	type entry struct {
		key   uint32
		value uint64
	}

	entries := make([]entry, 0, len(m.data))
	for k, v := range m.data {
		entries = append(entries, entry{key: k, value: v})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].value > entries[j].value
	})

	result := make([]uint32, 0, n)
	for i := 0; i < len(entries) && i < n; i++ {
		result = append(result, entries[i].key)
	}

	return result
}
