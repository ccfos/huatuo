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
	"encoding/binary"
	"fmt"
	"slices"
	"testing"
)

func BenchmarkStackAggregatesSortedAllocations(b *testing.B) {
	for _, test := range []struct {
		name                 string
		count, distinctBytes int
	}{
		{name: "small", count: 64, distinctBytes: 64},
		{name: "unique", count: 8192, distinctBytes: 8192},
		{name: "tied", count: 8192, distinctBytes: 8},
	} {
		b.Run(test.name, func(b *testing.B) {
			groups := newStackAggr()
			for i := range test.count {
				var stack [64]byte
				binary.LittleEndian.PutUint64(stack[:], uint64(i+1))
				if !groups.addSample(stack[:], 1, uint64((i*7919)%test.distinctBytes+1), 1) {
					b.Fatal("aggregate key budget exhausted")
				}
			}
			ctx := b.Context()
			b.ReportAllocs()
			for b.Loop() {
				allocations, err := groups.sortedAllocations(ctx)
				if err != nil || len(allocations) != test.count || allocations[0].inuseBytes != int64(test.distinctBytes) {
					b.Fatalf("sorted allocations count=%d: %+v, %v", test.count, allocations, err)
				}
			}
		})
	}
}

func BenchmarkStackAggregatesPipeline(b *testing.B) {
	const count = 8192
	stacks := make([][64]byte, count)
	for i := range stacks {
		binary.LittleEndian.PutUint64(stacks[i][:], uint64(i+1))
	}
	for _, repeats := range []int{1, 8} {
		b.Run(fmt.Sprintf("repeats=%d", repeats), func(b *testing.B) {
			ctx := b.Context()
			b.ReportAllocs()
			for b.Loop() {
				groups := newStackAggr()
				for range repeats {
					for i := range stacks {
						if !groups.addSample(stacks[i][:], 1, uint64((i*7919)%count+1), 1) {
							b.Fatal("aggregate key budget exhausted")
						}
					}
				}
				top, err := groups.sortedAllocations(ctx)
				if len(top) > 100 {
					top = slices.Clone(top[:100])
				}
				if err != nil || len(top) != 100 || top[0].inuseBytes != int64(count*repeats) || top[0].inuseObjects != int64(repeats) {
					b.Fatalf("aggregate pipeline repeats=%d: %+v, %v", repeats, top, err)
				}
			}
		})
	}
}
