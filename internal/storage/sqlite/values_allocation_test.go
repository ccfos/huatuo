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
	"context"
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/ccfos/huatuo/internal/storage/driver"
	storagesqlite "github.com/ccfos/huatuo/internal/storage/sqlite"
)

func newValuesAllocationBackend(t testing.TB, fields []map[string]any) *storagesqlite.Storage {
	t.Helper()
	backend, err := storagesqlite.NewBackend(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := backend.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if err := backend.Init(context.Background(), "values_allocation", []driver.Index{{Field: "name"}}); err != nil {
		t.Fatal(err)
	}
	for index, recordFields := range fields {
		if err := backend.Save(context.Background(), driver.Record{
			ID:     fmt.Sprintf("record-%d", index),
			Data:   []byte(`{}`),
			Fields: recordFields,
		}, driver.SaveOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	return backend
}

func TestSQLiteValuesAllocation(t *testing.T) {
	sparse := []map[string]any{
		{"name": "beta", "group": "selected"},
		{"name": "alpha", "group": "selected"},
		{"name": "beta", "group": "other"},
		{"name": "", "group": "other"},
		{"name": nil, "group": "selected"},
		{"group": "selected"},
	}
	growing := make([]map[string]any, 257)
	growingNames := make([]string, len(growing))
	for index := range growing {
		name := fmt.Sprintf("name-%03d", index)
		growing[index] = map[string]any{"name": name}
		growingNames[index] = name
	}

	tests := []struct {
		name   string
		fields []map[string]any
		query  driver.Query
		size   int
		want   []string
	}{
		{name: "empty_with_maximum_limit", size: math.MaxInt, want: []string{}},
		{name: "sparse_with_maximum_limit", fields: sparse, size: math.MaxInt, want: []string{"", "alpha", "beta"}},
		{
			name:   "filtered_with_maximum_limit",
			fields: sparse,
			query: driver.Query{Filters: []driver.Filter{
				{Field: "group", Op: driver.OpEq, Value: "selected"},
			}},
			size: math.MaxInt,
			want: []string{"alpha", "beta"},
		},
		{name: "grows_with_actual_results", fields: growing, size: math.MaxInt, want: growingNames},
		{
			name:   "ignores_record_pagination",
			fields: sparse,
			query:  driver.Query{Limit: 1, Offset: 50, BatchSize: 1, Sorts: []driver.Sort{{Field: "name", Desc: true}}},
			size:   math.MaxInt,
			want:   []string{"", "alpha", "beta"},
		},
		{name: "honors_small_limit", fields: sparse, size: 2, want: []string{"", "alpha"}},
		{name: "ordinary_limit", fields: sparse, size: 100, want: []string{"", "alpha", "beta"}},
		{name: "empty_ordinary_limit", size: 100, want: []string{}},
	}
	for index := range tests {
		test := &tests[index]
		t.Run(test.name, func(t *testing.T) {
			backend := newValuesAllocationBackend(t, test.fields)
			defer func() {
				if failure := recover(); failure != nil {
					t.Fatalf("Values(size=%d) panicked: %v", test.size, failure)
				}
			}()
			got, err := backend.Values(t.Context(), "name", test.query, test.size)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Errorf("Values() = %v, want %v", got, test.want)
			}
		})
	}
}

func BenchmarkSQLiteValuesSparseLimit(b *testing.B) {
	for _, size := range []int{100, 1_000_000} {
		b.Run(fmt.Sprintf("limit_%d", size), func(b *testing.B) {
			backend := newValuesAllocationBackend(b, []map[string]any{
				{"name": "alpha"},
				{"name": "beta"},
				{"name": "gamma"},
			})
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				values, err := backend.Values(b.Context(), "name", driver.Query{}, size)
				if err != nil {
					b.Fatal(err)
				}
				if len(values) != 3 {
					b.Fatalf("Values() returned %d values, want 3", len(values))
				}
			}
		})
	}
}
