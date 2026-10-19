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
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	"github.com/ccfos/huatuo/internal/storage/driver"
)

func TestSQLiteReadBackNumericPredicates(t *testing.T) {
	backend := newSQLiteBackendForTest(t)
	if backend == nil {
		t.Fatal("create SQLite backend")
	}
	if err := backend.Init(t.Context(), "numeric_roundtrip", []driver.Index{{Field: "revision"}}); err != nil {
		t.Fatal(err)
	}
	const first int64 = 9007199254740993
	for index, id := range []string{"a", "b", "c"} {
		if err := backend.Save(t.Context(), driver.Record{
			ID: id, Data: []byte(`{}`), Fields: map[string]any{"revision": first + int64(index)},
		}, driver.SaveOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := backend.Get(t.Context(), "b")
	if err != nil {
		t.Fatal(err)
	}
	value := stored.Fields["revision"]
	if value != json.Number("9007199254740994") {
		t.Fatalf("read-back revision = %#v", value)
	}
	for _, test := range []struct {
		name  string
		op    driver.Op
		value any
		want  []string
	}{
		{"equal", driver.OpEq, value, []string{"b"}},
		{"not equal", driver.OpNe, value, []string{"a", "c"}},
		{"greater", driver.OpGt, value, []string{"c"}},
		{"greater or equal", driver.OpGte, value, []string{"b", "c"}},
		{"less", driver.OpLt, value, []string{"a"}},
		{"less or equal", driver.OpLte, value, []string{"a", "b"}},
		{"in", driver.OpIn, []json.Number{"9007199254740993", "9007199254740995"}, []string{"a", "c"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			query := driver.Query{Filters: []driver.Filter{{Field: "revision", Op: test.op, Value: test.value}}, Limit: 10, BatchSize: 1}
			var ids []string
			if err := backend.Query(t.Context(), query, func(records []driver.Record) error {
				for _, record := range records {
					ids = append(ids, record.ID)
				}
				return nil
			}); err != nil {
				t.Fatalf("query using read-back number: %v", err)
			}
			sort.Strings(ids)
			if !reflect.DeepEqual(ids, test.want) {
				t.Fatalf("query IDs = %v, want %v", ids, test.want)
			}
			count, err := backend.Count(t.Context(), query)
			if err != nil || count != int64(len(test.want)) {
				t.Fatalf("count = (%d, %v), want %d", count, err, len(test.want))
			}
			terms, err := backend.Values(t.Context(), "id", query, 10)
			if err != nil || !reflect.DeepEqual(terms, test.want) {
				t.Fatalf("values = (%v, %v), want %v", terms, err, test.want)
			}
		})
	}
	t.Run("conditional save", func(t *testing.T) {
		stored.Fields["revision"] = first + 3
		if err := backend.Save(t.Context(), stored, driver.SaveOptions{
			Mode:       driver.SaveModeConditional,
			Conditions: []driver.Filter{{Field: "revision", Op: driver.OpEq, Value: value}},
		}); err != nil {
			t.Fatalf("conditional save using read-back revision: %v", err)
		}
		updated, err := backend.Get(t.Context(), stored.ID)
		if err != nil || updated.Fields["revision"] != json.Number("9007199254740996") {
			t.Fatalf("updated record = (%#v, %v)", updated, err)
		}
	})
	t.Run("delete query", func(t *testing.T) {
		deleted, err := backend.DeleteByQuery(t.Context(), driver.DeleteQuery{
			Filters: []driver.Filter{{Field: "revision", Op: driver.OpEq, Value: json.Number("9007199254740993")}},
		})
		if err != nil || deleted != 1 {
			t.Fatalf("delete using JSON number = (%d, %v), want (1, nil)", deleted, err)
		}
		remaining, err := backend.Count(t.Context(), driver.Query{})
		if err != nil || remaining != 2 {
			t.Fatalf("remaining count = (%d, %v), want (2, nil)", remaining, err)
		}
	})
}
