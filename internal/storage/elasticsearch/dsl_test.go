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

package elasticsearch

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/ccfos/huatuo/internal/storage/driver"
	"github.com/ccfos/huatuo/internal/timeutil"
)

func TestBuildClauseComparisons(t *testing.T) {
	tests := []struct {
		name   string
		filter driver.Filter
		want   string
	}{
		{
			"empty equal",
			driver.Filter{Field: "label", Op: driver.OpEq, Value: ""},
			`{"term":{"label":{"value":""}}}`,
		},
		{
			"empty not equal",
			driver.Filter{Field: "label", Op: driver.OpNe, Value: ""},
			`{"bool":{"filter":[{"exists":{"field":"label"}}],"must_not":[{"term":{"label":{"value":""}}}]}}`,
		},
		{
			"keyword equal",
			driver.Filter{Field: "label.keyword", Op: driver.OpEq, Value: "running"},
			`{"term":{"label.keyword":{"value":"running"}}}`,
		},
		{
			"keyword not equal",
			driver.Filter{Field: "label.keyword", Op: driver.OpNe, Value: "running"},
			`{"bool":{"filter":[{"exists":{"field":"label.keyword"}}],"must_not":[{"term":{"label.keyword":{"value":"running"}}}]}}`,
		},
		{
			"native keyword",
			driver.Filter{Field: "label", Op: driver.OpEq, Value: "running"},
			`{"term":{"label":{"value":"running"}}}`,
		},
		{
			"in",
			driver.Filter{Field: "label.keyword", Op: driver.OpIn, Value: []string{"running", ""}},
			`{"terms":{"label.keyword":["running",""]}}`,
		},
		{
			"native keyword in",
			driver.Filter{Field: "label", Op: driver.OpIn, Value: []string{"running"}},
			`{"terms":{"label":["running"]}}`,
		},
		{
			"exists",
			driver.Filter{Field: "label", Op: driver.OpExists},
			`{"exists":{"field":"label"}}`,
		},
		{
			"not exists",
			driver.Filter{Field: "label", Op: driver.OpNotExists},
			`{"bool":{"must_not":[{"exists":{"field":"label"}}]}}`,
		},
		{
			"false",
			driver.Filter{Field: "enabled", Op: driver.OpEq, Value: false},
			`{"term":{"enabled":{"value":false}}}`,
		},
		{
			"large integer equal",
			driver.Filter{Field: "n", Op: driver.OpEq, Value: int64(9007199254740993)},
			`{"term":{"n":{"value":9007199254740993}}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clause, err := buildClause(test.filter)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(clause)
			if err != nil {
				t.Fatal(err)
			}
			assertQueryJSON(t, raw, []byte(test.want))
		})
	}
}

func TestBuildClauseRangePrecision(t *testing.T) {
	instant := time.Date(2026, 10, 8, 0, 0, 0, 123456789, time.UTC)
	values := []struct {
		name  string
		value any
		want  string
	}{
		{"large int", int64(9007199254740993), "9007199254740993"},
		{"min int", int64(math.MinInt64), "-9223372036854775808"},
		{"max int", int64(math.MaxInt64), "9223372036854775807"},
		{"max uint", uint64(math.MaxUint64), "18446744073709551615"},
		{"float", 1.5, "1.5"},
		{"string", "running", `"running"`},
		{"time", instant, `"2026-10-08T00:00:00.123456789Z"`},
		{"timestamp", timeutil.Timestamp{Time: instant}, `"2026-10-08T00:00:00.123456789Z"`},
	}
	for _, op := range []driver.Op{driver.OpGt, driver.OpGte, driver.OpLt, driver.OpLte} {
		for _, test := range values {
			t.Run(string(op)+"/"+test.name, func(t *testing.T) {
				clause, err := buildClause(driver.Filter{Field: "value", Op: op, Value: test.value})
				if err != nil {
					t.Fatal(err)
				}
				raw, err := json.Marshal(clause)
				if err != nil {
					t.Fatal(err)
				}
				want := fmt.Sprintf(`{"range":{"value":{%q:%s}}}`, op, test.want)
				assertQueryJSON(t, raw, []byte(want))
			})
		}
	}
}

func TestBuildRequestsShareFilterSemantics(t *testing.T) {
	filters := []driver.Filter{
		{Field: "label", Op: driver.OpEq, Value: ""},
		{Field: "container", Op: driver.OpNotExists},
		{Field: "n", Op: driver.OpNe, Value: 1},
	}
	want := []byte(`{"bool":{"filter":[{"term":{"label":{"value":""}}},{"bool":{"must_not":[{"exists":{"field":"container"}}]}},{"bool":{"filter":[{"exists":{"field":"n"}}],"must_not":[{"term":{"n":{"value":1}}}]}}]}}`)
	builders := []struct {
		name  string
		build func([]driver.Filter) ([]byte, error)
	}{
		{"search", func(f []driver.Filter) ([]byte, error) {
			return buildSearchRequest(driver.Query{Filters: f, Limit: 10})
		}},
		{"count", func(f []driver.Filter) ([]byte, error) { return buildCountRequest(driver.Query{Filters: f}) }},
		{"delete", func(f []driver.Filter) ([]byte, error) {
			return buildDeleteByQueryRequest(driver.DeleteQuery{Filters: f})
		}},
		{"values", func(f []driver.Filter) ([]byte, error) {
			return buildValuesRequest("label", driver.Query{Filters: f}, 10)
		}},
	}
	for _, test := range builders {
		t.Run(test.name, func(t *testing.T) {
			raw, err := test.build(filters)
			if err != nil {
				t.Fatal(err)
			}
			var body struct {
				Query json.RawMessage `json:"query"`
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			assertQueryJSON(t, body.Query, want)
			if _, err := test.build([]driver.Filter{{Field: "label", Op: driver.OpEq}}); !errors.Is(err, driver.ErrInvalidQuery) {
				t.Fatalf("null comparison error = %v, want ErrInvalidQuery", err)
			}
		})
	}
}

func assertQueryJSON(t *testing.T, got, want []byte) {
	t.Helper()
	decode := func(raw []byte) any {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if diff := cmp.Diff(decode(want), decode(got)); diff != "" {
		t.Fatalf("query mismatch (-want +got):\n%s", diff)
	}
}
