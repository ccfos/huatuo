// Copyright 2026
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sqlite

import (
	"context"
	"testing"

	"github.com/ccfos/huatuo/internal/storage/driver"
)

// TestSaveConditionalStatementsByShape runs sequential conditional saves
// with different fields, operators, and predicate counts against one
// backend. The statement cache must key on the SQL text, not reuse the
// first prepared statement for every shape.
func TestSaveConditionalStatementsByShape(t *testing.T) {
	s, err := NewBackend(t.TempDir() + "/shape.db")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	defer s.Close(ctx)
	if err := s.Init(ctx, "metrics", []driver.Index{
		{Field: "name"},
		{Field: "value"},
		{Field: "host"},
	}); err != nil {
		t.Fatal(err)
	}

	save := func(id string, fields map[string]any, conditions []driver.Filter) error {
		return s.Save(ctx, driver.Record{ID: id, Data: []byte("{}"), Fields: fields}, driver.SaveOptions{
			Mode:       driver.SaveModeConditional,
			Conditions: conditions,
		})
	}

	seed := map[string]any{"name": "seed", "value": 0, "host": "seedhost"}
	if err := s.Save(ctx, driver.Record{ID: "seed", Data: []byte("{}"), Fields: seed}, driver.SaveOptions{Mode: driver.SaveModeUpsert}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name       string
		conditions []driver.Filter
	}{
		{
			name:       "single equality",
			conditions: []driver.Filter{{Field: "name", Op: driver.OpEq, Value: "seed"}},
		},
		{
			name:       "different field",
			conditions: []driver.Filter{{Field: "host", Op: driver.OpEq, Value: "seedhost"}},
		},
		{
			name: "two predicates",
			conditions: []driver.Filter{
				{Field: "name", Op: driver.OpEq, Value: "seed"},
				{Field: "host", Op: driver.OpEq, Value: "seedhost"},
			},
		},
		{
			name:       "operator not-equal",
			conditions: []driver.Filter{{Field: "name", Op: driver.OpNe, Value: "other"}},
		},
	}

	for i, tc := range cases {
		fields := map[string]any{"name": "seed", "value": i, "host": "seedhost"}
		if err := save("seed", fields, tc.conditions); err != nil {
			t.Fatalf("%s: save failed: %v", tc.name, err)
		}
	}

	// Re-run the first shape after the others: the cached statement for
	// it must still carry its own predicate.
	fields := map[string]any{"name": "seed", "value": 99, "host": "seedhost"}
	if err := save("seed", fields,
		[]driver.Filter{{Field: "name", Op: driver.OpEq, Value: "seed"}}); err != nil {
		t.Fatalf("replay first shape: %v", err)
	}
}
