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
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/storage/driver"
)

func TestBuildRangeClausePreservesNumericBounds(t *testing.T) {
	for _, op := range []driver.Op{driver.OpGt, driver.OpGte, driver.OpLt, driver.OpLte} {
		t.Run(string(op), func(t *testing.T) {
			for _, test := range []struct {
				name  string
				value any
				want  string
			}{
				{"int", int(-42), "-42"},
				{"int8", int8(-128), "-128"},
				{"int16", int16(-32768), "-32768"},
				{"int32", int32(-2147483648), "-2147483648"},
				{"int64 above float precision", int64(9007199254740993), "9007199254740993"},
				{"int64 below float precision", int64(-9007199254740993), "-9007199254740993"},
				{"int64 maximum", int64(9223372036854775807), "9223372036854775807"},
				{"int64 minimum", int64(-9223372036854775808), "-9223372036854775808"},
				{"uint", uint(42), "42"},
				{"uint8", uint8(255), "255"},
				{"uint16", uint16(65535), "65535"},
				{"uint32", uint32(4294967295), "4294967295"},
				{"uint64 above float precision", uint64(9007199254740993), "9007199254740993"},
				{"uint64 maximum", uint64(18446744073709551615), "18446744073709551615"},
				{"float32", float32(0.125), "0.125"},
				{"float64", float64(0.125), "0.125"},
				{"date", "2026-10-07T00:00:00Z", `"2026-10-07T00:00:00Z"`},
			} {
				t.Run(test.name, func(t *testing.T) {
					clause, err := buildRangeClause(driver.Filter{Field: "priority", Op: op, Value: test.value})
					if err != nil {
						t.Fatal(err)
					}
					encoded, err := json.Marshal(clause)
					if err != nil {
						t.Fatal(err)
					}
					want := fmt.Sprintf(`{%q:%s}`, op, test.want)
					if string(encoded) != want {
						t.Errorf("range JSON = %s, want %s", encoded, want)
					}
				})
			}
		})
	}
}

func TestBackendQueryPreservesIntegerRangeBoundary(t *testing.T) {
	const boundary = int64(9007199254740993)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/" {
			if _, err := w.Write([]byte(`{}`)); err != nil {
				t.Errorf("write server info: %v", err)
			}
			return
		}
		if r.URL.Path != "/precision/_search" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var request struct {
			Query struct {
				Bool struct {
					Filter []struct {
						Range map[string]map[string]json.Number `json:"range"`
					} `json:"filter"`
				} `json:"bool"`
			} `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode range request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if len(request.Query.Bool.Filter) != 1 {
			t.Errorf("query has %d filters, want 1", len(request.Query.Bool.Filter))
			http.Error(w, "invalid filters", http.StatusBadRequest)
			return
		}
		threshold, err := request.Query.Bool.Filter[0].Range["priority"]["gt"].Int64()
		if err != nil {
			t.Errorf("parse integer range boundary: %v", err)
			http.Error(w, "invalid boundary", http.StatusBadRequest)
			return
		}
		hits := make([]map[string]any, 0, 2)
		for _, document := range []struct {
			id       string
			priority int64
		}{
			{"boundary", boundary},
			{"above", boundary + 1},
		} {
			if document.priority > threshold {
				hits = append(hits, map[string]any{
					"_id":     document.id,
					"_source": map[string]any{"priority": document.priority},
				})
			}
		}
		response := map[string]any{
			"timed_out": false,
			"_shards":   map[string]int{"total": 1, "successful": 1, "failed": 0},
			"hits":      map[string]any{"hits": hits},
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("write search response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	backend, err := NewBackend(&Config{Addresses: []string{server.URL}, Index: "precision"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := backend.Close(ctx); err != nil {
			t.Errorf("close backend: %v", err)
		}
	})
	records, err := backend.Query(t.Context(), driver.Query{
		Filters: []driver.Filter{{Field: "priority", Op: driver.OpGt, Value: boundary}},
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ID != "above" {
		t.Fatalf("Query(priority > %d) returned %v, want only the record above the boundary", boundary, records)
	}
}
