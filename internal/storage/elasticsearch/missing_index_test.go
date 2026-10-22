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
	"net/http"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/storage/driver"
)

func TestMissingIndexResponses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		missing bool
	}{
		{"missing index", `{"error":{"type":"index_not_found_exception","reason":"missing index"},"status":404}`, true},
		{"proxy route", "upstream route not found", false},
		{"other resource", `{"error":{"type":"resource_not_found_exception","reason":"missing search resource"},"status":404}`, false},
		{"malformed error", `{"error":`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := newResponseBackend(t, http.StatusNotFound, tc.body)
			operations := map[string]func() error{
				"query": func() error {
					return backend.Query(t.Context(), driver.Query{Limit: 1}, func([]driver.Record) error {
						t.Fatal("404 response delivered records")
						return nil
					})
				},
				"count":  func() error { _, err := backend.Count(t.Context(), driver.Query{}); return err },
				"values": func() error { _, err := backend.Values(t.Context(), "hostname", driver.Query{}, 10); return err },
				"delete by query": func() error {
					_, err := backend.DeleteByQuery(t.Context(), driver.DeleteQuery{Filters: []driver.Filter{{Field: "hostname", Op: driver.OpEq, Value: "node"}}})
					return err
				},
			}
			for name, operation := range operations {
				t.Run(name, func(t *testing.T) {
					err := operation()
					if tc.missing {
						if err != nil {
							t.Fatalf("missing index returned error: %v", err)
						}
					} else if err == nil {
						t.Fatal("non-index 404 reported success")
					} else if !strings.Contains(err.Error(), "404") {
						t.Fatalf("error omitted HTTP status: %v", err)
					}
				})
			}
		})
	}
}
