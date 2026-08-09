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

package clickhouse

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"huatuo-bamai/internal/storage/driver"
)

func TestStorageInitAndSave(t *testing.T) {
	type request struct {
		query, body, username, password string
	}
	requests := make(chan request, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		username, password, _ := r.BasicAuth()
		requests <- request{r.URL.Query().Get("query"), string(body), username, password}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	backend, err := NewBackend(server.URL, "huatuo", "secret", "observability", "events")
	if err != nil {
		t.Fatalf("NewBackend() error = %v", err)
	}
	if err := backend.Init(t.Context(), "tracing_documents", nil); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if err := backend.Save(t.Context(), driver.Record{
		ID:     "trace-20260809",
		Data:   []byte(`{"tracer_name":"oom"}`),
		Fields: map[string]any{"tracer_name": "oom", "region": "cn-beijing"},
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	create := <-requests
	if !strings.Contains(create.query, "CREATE TABLE IF NOT EXISTS `observability`.`events`") {
		t.Errorf("create query = %q", create.query)
	}
	insert := <-requests
	if !strings.Contains(insert.query, "FORMAT JSONEachRow") {
		t.Errorf("insert query = %q", insert.query)
	}
	if insert.username != "huatuo" || insert.password != "secret" {
		t.Errorf("basic auth = %q/%q", insert.username, insert.password)
	}
	var row struct {
		Collection string `json:"collection"`
		ID         string `json:"id"`
		Data       string `json:"data"`
		Fields     string `json:"fields"`
	}
	if err := json.Unmarshal([]byte(insert.body), &row); err != nil {
		t.Fatalf("decode row: %v", err)
	}
	if row.Collection != "tracing_documents" || row.ID != "trace-20260809" || row.Data != `{"tracer_name":"oom"}` {
		t.Errorf("row = %+v", row)
	}
	if !strings.Contains(row.Fields, `"region":"cn-beijing"`) {
		t.Errorf("fields = %q", row.Fields)
	}
}

func TestStorageReportsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	backend, err := NewBackend(server.URL, "", "", "default", "events")
	if err != nil {
		t.Fatalf("NewBackend() error = %v", err)
	}
	err = backend.Init(t.Context(), "tracing_documents", nil)
	if err == nil || !strings.Contains(err.Error(), "database unavailable") {
		t.Fatalf("Init() error = %v", err)
	}
}

func TestStorageValidationAndUnsupportedOperations(t *testing.T) {
	if _, err := NewBackend("tcp://localhost:9000", "", "", "default", "events"); err == nil {
		t.Fatal("NewBackend() error = nil for non-HTTP address")
	}
	if _, err := NewBackend("http://localhost:8123", "", "", "bad-name", "events"); err == nil {
		t.Fatal("NewBackend() error = nil for invalid database")
	}
	backend := &Storage{}
	if err := backend.Save(t.Context(), driver.Record{}); !errors.Is(err, driver.ErrInvalidField) {
		t.Fatalf("Save() error = %v, want ErrInvalidField", err)
	}
	if _, err := backend.Get(t.Context(), "id"); !errors.Is(err, driver.ErrUnsupported) {
		t.Fatalf("Get() error = %v, want ErrUnsupported", err)
	}
}
