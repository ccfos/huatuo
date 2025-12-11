package main

import "sort"

type TableSort struct {
	data map[uint32]uint64
}

func NewTableSort() *TableSort {
	return &TableSort{
		data: make(map[uint32]uint64),
	}
}

func (m *TableSort) Update(key uint32, value uint64) {
	m.data[key] += value
}

func (m *TableSort) TopKeyN(n int) []uint32 {
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
