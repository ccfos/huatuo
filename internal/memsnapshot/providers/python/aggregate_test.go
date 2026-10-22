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
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

func TestSnapshotTopK(t *testing.T) {
	c := &scanner{aggregates: map[uint64]*memsnapshot.ObjectAggregate{
		1: {TypeName: "b", Count: 2, ShallowBytes: 100},
		2: {TypeName: "a", Count: 1, ShallowBytes: 100},
		3: {TypeName: "a", Count: 3, ShallowBytes: 100},
		4: {TypeName: "large", Count: 1, ShallowBytes: 200},
	}}
	all := fullSortEntries(c)
	for _, limit := range []int{1, 2, 4, 10} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			result := c.buildSnapshot(limit)
			want := all[:min(limit, len(all))]
			if !reflect.DeepEqual(result.Entries, want) || result.OutputTruncated != (len(all) > limit) {
				t.Fatalf("snapshot = %+v, want entries %+v", result, want)
			}
			if result.Status != memsnapshot.SnapshotStatusComplete {
				t.Fatalf("status = %s", result.Status)
			}
		})
	}
	c.partial = "scan stopped"
	if result := c.buildSnapshot(1); result.Status != memsnapshot.SnapshotStatusPartial || !result.OutputTruncated {
		t.Fatalf("partial snapshot lost status or output limit: %+v", result)
	}
	c.aggregates = nil
	if result := c.buildSnapshot(10); len(result.Entries) != 0 || result.OutputTruncated {
		t.Fatal(result)
	}
}

func BenchmarkEntries(b *testing.B) {
	for _, count := range []int{100, 32768} {
		c := &scanner{aggregates: make(map[uint64]*memsnapshot.ObjectAggregate, count)}
		for i := 0; i < count; i++ {
			c.aggregates[uint64(i)] = &memsnapshot.ObjectAggregate{TypeName: fmt.Sprint(i), Count: uint64(i%10 + 1), ShallowBytes: uint64((i * 7919) % count)}
		}
		for _, limit := range []int{10, 100} {
			b.Run(fmt.Sprintf("types%d/top%d", count, limit), func(b *testing.B) {
				b.Run("sort", func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						if len(fullSortEntries(c)) < limit {
							b.Fatal("missing entries")
						}
					}
				})
				b.Run("heap", func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						if len(c.entries(limit)) != limit {
							b.Fatal("missing entries")
						}
					}
				})
			})
		}
	}
}

func fullSortEntries(c *scanner) []memsnapshot.Entry {
	result := make([]memsnapshot.Entry, 0, len(c.aggregates))
	for _, aggregate := range c.aggregates {
		if aggregate.Count != 0 {
			aggregate.AverageBytes = float64(aggregate.ShallowBytes) /
				float64(aggregate.Count)
		}
		result = append(result, memsnapshot.Entry{
			Kind: "gc_tracked_object_type", Name: aggregate.TypeName,
			Bytes: aggregate.ShallowBytes, Objects: aggregate.Count,
			AverageBytes: aggregate.AverageBytes,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Bytes != result[j].Bytes {
			return result[i].Bytes > result[j].Bytes
		}
		if result[i].Name != result[j].Name {
			return result[i].Name < result[j].Name
		}
		return result[i].Objects > result[j].Objects
	})
	return result
}
