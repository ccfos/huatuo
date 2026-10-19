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
	"errors"
	"slices"
	"testing"

	"github.com/ccfos/huatuo/internal/storage/driver"
)

func TestSQLiteFilterSemantics(t *testing.T) {
	tests := []struct {
		name   string
		filter driver.Filter
		want   []string
	}{
		{"empty equal", driver.Filter{Field: "label", Op: driver.OpEq, Value: ""}, []string{"empty"}},
		{"empty not equal", driver.Filter{Field: "label", Op: driver.OpNe, Value: ""}, []string{"exact", "phrase"}},
		{"equal", driver.Filter{Field: "label", Op: driver.OpEq, Value: "running"}, []string{"exact"}},
		{"not equal", driver.Filter{Field: "label", Op: driver.OpNe, Value: "running"}, []string{"empty", "phrase"}},
		{"in", driver.Filter{Field: "label", Op: driver.OpIn, Value: []string{"running", ""}}, []string{"empty", "exact"}},
		{"exists", driver.Filter{Field: "label", Op: driver.OpExists}, []string{"empty", "exact", "phrase"}},
		{"not exists", driver.Filter{Field: "label", Op: driver.OpNotExists}, []string{"absent", "null"}},
		{"greater", driver.Filter{Field: "n", Op: driver.OpGt, Value: int64(9007199254740993)}, []string{"phrase"}},
		{"greater equal", driver.Filter{Field: "n", Op: driver.OpGte, Value: int64(9007199254740993)}, []string{"exact", "phrase"}},
		{"less", driver.Filter{Field: "n", Op: driver.OpLt, Value: int64(9007199254740993)}, []string{"empty"}},
		{"less equal", driver.Filter{Field: "n", Op: driver.OpLte, Value: int64(9007199254740993)}, []string{"empty", "exact"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := newSQLiteBackendForTest(t)
			if backend == nil {
				t.Fatal("SQLite backend unavailable")
			}
			if err := backend.Init(t.Context(), "filters", nil); err != nil {
				t.Fatal(err)
			}
			seedSQLiteRecords(t, backend, []driver.Record{
				{ID: "absent", Data: []byte(`{}`)},
				{ID: "null", Data: []byte(`{}`), Fields: map[string]any{"label": nil}},
				{ID: "empty", Data: []byte(`{}`), Fields: map[string]any{"label": "", "n": int64(9007199254740992)}},
				{ID: "exact", Data: []byte(`{}`), Fields: map[string]any{"label": "running", "n": int64(9007199254740993)}},
				{ID: "phrase", Data: []byte(`{}`), Fields: map[string]any{"label": "running fast", "n": int64(9007199254740994)}},
			})
			filters := []driver.Filter{test.filter}
			records, err := queryRecords(t.Context(), backend, driver.Query{
				Filters: filters,
				Sorts:   []driver.Sort{{Field: "id"}},
				Limit:   10,
			})
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, record := range records {
				ids = append(ids, record.ID)
			}
			if !slices.Equal(ids, test.want) {
				t.Fatalf("Query IDs = %v, want %v", ids, test.want)
			}
			count, err := backend.Count(t.Context(), driver.Query{Filters: filters})
			if err != nil || count != int64(len(test.want)) {
				t.Fatalf("Count = (%d, %v), want (%d, nil)", count, err, len(test.want))
			}
			deleted, err := backend.DeleteByQuery(t.Context(), driver.DeleteQuery{Filters: filters})
			if err != nil || deleted != count {
				t.Fatalf("DeleteByQuery = (%d, %v), want (%d, nil)", deleted, err, count)
			}
			for _, id := range test.want {
				if _, err := backend.Get(t.Context(), id); !errors.Is(err, driver.ErrNotFound) {
					t.Fatalf("Get(%s) after delete = %v, want ErrNotFound", id, err)
				}
			}
		})
	}
}

func TestSQLiteRejectsNullComparison(t *testing.T) {
	backend := newSQLiteBackendForTest(t)
	if backend == nil {
		t.Fatal("SQLite backend unavailable")
	}
	if err := backend.Init(t.Context(), "filters", nil); err != nil {
		t.Fatal(err)
	}
	query := driver.Query{Filters: []driver.Filter{{Field: "label", Op: driver.OpEq}}, Limit: 10}
	if _, err := queryRecords(t.Context(), backend, query); !errors.Is(err, driver.ErrInvalidQuery) {
		t.Fatalf("Query error = %v, want ErrInvalidQuery", err)
	}
	if _, err := backend.Count(t.Context(), query); !errors.Is(err, driver.ErrInvalidQuery) {
		t.Fatalf("Count error = %v, want ErrInvalidQuery", err)
	}
	if _, err := backend.Values(t.Context(), "label", query, 10); !errors.Is(err, driver.ErrInvalidQuery) {
		t.Fatalf("Values error = %v, want ErrInvalidQuery", err)
	}
	if _, err := backend.DeleteByQuery(t.Context(), driver.DeleteQuery{Filters: query.Filters}); !errors.Is(err, driver.ErrInvalidQuery) {
		t.Fatalf("DeleteByQuery error = %v, want ErrInvalidQuery", err)
	}
}
