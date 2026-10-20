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

package sqlite_test

import (
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/ccfos/huatuo/internal/storage/driver"
)

func queryAllocationFields(count int) []map[string]any {
	fields := make([]map[string]any, count)
	for index := range fields {
		fields[index] = map[string]any{
			"name":  fmt.Sprintf("name-%03d", index),
			"group": "other",
		}
		if index%2 == 0 {
			fields[index]["group"] = "selected"
		}
	}
	return fields
}

func TestSQLiteQueryAllocation(t *testing.T) {
	indices := func(start, end, step int) []int {
		result := []int{}
		for index := start; index < end; index += step {
			result = append(result, index)
		}
		return result
	}
	tests := []struct {
		name        string
		rows        int
		query       driver.Query
		wantIndices []int
		wantBatches []int
	}{
		{
			name:  "empty_with_maximum_batch",
			query: driver.Query{Limit: math.MaxInt, BatchSize: math.MaxInt},
		},
		{
			name: "sparse_with_maximum_batch", rows: 3,
			query:       driver.Query{Limit: math.MaxInt, BatchSize: math.MaxInt},
			wantIndices: indices(0, 3, 1), wantBatches: []int{3},
		},
		{
			name: "grows_to_actual_results", rows: 257,
			query:       driver.Query{Limit: math.MaxInt, BatchSize: math.MaxInt},
			wantIndices: indices(0, 257, 1), wantBatches: []int{257},
		},
		{
			name: "filters_with_maximum_batch", rows: 10,
			query: driver.Query{Limit: math.MaxInt, BatchSize: math.MaxInt, Filters: []driver.Filter{
				{Field: "group", Op: driver.OpEq, Value: "selected"},
			}},
			wantIndices: indices(0, 10, 2), wantBatches: []int{5},
		},
		{
			name: "offset_with_maximum_batch", rows: 10,
			query:       driver.Query{Limit: math.MaxInt, BatchSize: math.MaxInt, Offset: 2},
			wantIndices: indices(2, 10, 1), wantBatches: []int{8},
		},
		{
			name: "small_limit_bounds_batch", rows: 5,
			query:       driver.Query{Limit: 2, BatchSize: math.MaxInt},
			wantIndices: indices(0, 2, 1), wantBatches: []int{2},
		},
		{
			name:  "maximum_limit_with_default_batch",
			query: driver.Query{Limit: math.MaxInt},
		},
		{
			name: "preserves_requested_boundaries_and_retained_batches", rows: 600,
			query:       driver.Query{Limit: 600, BatchSize: 257},
			wantIndices: indices(0, 600, 1), wantBatches: []int{257, 257, 86},
		},
	}
	for index := range tests {
		test := &tests[index]
		t.Run(test.name, func(t *testing.T) {
			backend := newValuesAllocationBackend(t, queryAllocationFields(test.rows))
			defer func() {
				if failure := recover(); failure != nil {
					t.Fatalf("Query(batch=%d) panicked: %v", test.query.BatchSize, failure)
				}
			}()
			test.query.Sorts = []driver.Sort{{Field: "name"}}
			var batches [][]driver.Record
			err := backend.Query(t.Context(), test.query, func(records []driver.Record) error {
				// Retain the actual slices to detect reuse after later callbacks.
				batches = append(batches, records)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			var sizes []int
			var records []driver.Record
			for _, batch := range batches {
				sizes = append(sizes, len(batch))
				records = append(records, batch...)
			}
			if !reflect.DeepEqual(sizes, test.wantBatches) {
				t.Errorf("batch sizes = %v, want %v", sizes, test.wantBatches)
			}
			if len(records) != len(test.wantIndices) {
				t.Fatalf("record count = %d, want %d", len(records), len(test.wantIndices))
			}
			for index := range records {
				record := &records[index]
				wantIndex := test.wantIndices[index]
				if record.ID != fmt.Sprintf("record-%d", wantIndex) || record.Fields["name"] != fmt.Sprintf("name-%03d", wantIndex) || string(record.Data) != "{}" {
					t.Errorf("record %d = %+v, want original record %d", index, record, wantIndex)
				}
			}
		})
	}
}

func BenchmarkSQLiteQueryAllocation(b *testing.B) {
	for _, test := range []struct {
		name  string
		rows  int
		batch int
	}{
		{name: "sparse_default", rows: 3},
		{name: "sparse_large", rows: 3, batch: 1_000_000},
		{name: "dense_batches", rows: 600, batch: 257},
	} {
		b.Run(test.name, func(b *testing.B) {
			backend := newValuesAllocationBackend(b, queryAllocationFields(test.rows))
			query := driver.Query{Limit: math.MaxInt, BatchSize: test.batch, Sorts: []driver.Sort{{Field: "name"}}}
			b.ReportAllocs()
			for b.Loop() {
				count := 0
				if err := backend.Query(b.Context(), query, func(records []driver.Record) error {
					count += len(records)
					return nil
				}); err != nil {
					b.Fatal(err)
				}
				if count != test.rows {
					b.Fatalf("Query() returned %d records, want %d", count, test.rows)
				}
			}
		})
	}
}
