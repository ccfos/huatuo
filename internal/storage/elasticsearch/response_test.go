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
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/elastic/go-elasticsearch/v8/esapi"
	"github.com/elastic/go-elasticsearch/v8/esutil"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/storage/driver"
)

func newResponseBackend(t *testing.T, status int, body string) *Storage {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/" {
			_, _ = io.WriteString(w, `{"version":{"number":"8.17.1"}}`)
			return
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	backend, err := NewBackend(&Config{Addresses: []string{server.URL}, Index: "profiles"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Testing cancels t.Context before running cleanup callbacks.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := backend.Close(ctx); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	return backend
}

func TestStorageSearchStatus(t *testing.T) {
	tests := []struct {
		name            string
		isTimedOut      bool
		shards          string
		expectedError   []string
		unexpectedError string
	}{
		{
			name:   "complete",
			shards: `{"total":2,"successful":2,"failed":0}`,
		},
		{
			name:          "timeout",
			isTimedOut:    true,
			shards:        `{"total":2,"successful":2,"failed":0}`,
			expectedError: []string{"timed out"},
		},
		{
			name:          "failed shard",
			shards:        `{"total":2,"successful":1,"failed":1,"failures":[{"index":"profiles","shard":1,"reason":{"type":"query_shard_exception","reason":"invalid range","caused_by":{"type":"number_format_exception","reason":"not a number"}}}]}`,
			expectedError: []string{"failed on 1 shards", "index=profiles shard=1", "query_shard_exception", "invalid range", "number_format_exception", "not a number"},
		},
		{
			name:            "timeout and failed shard",
			isTimedOut:      true,
			shards:          `{"total":2,"successful":1,"failed":1}`,
			expectedError:   []string{"timed out"},
			unexpectedError: "failed on",
		},
		{
			name:            "multiple failed shards",
			shards:          `{"total":3,"successful":1,"failed":2,"failures":[{"index":"profiles","shard":1,"reason":{"type":"query_shard_exception","reason":"first failure"}},{"index":"profiles","shard":2,"reason":{"type":"query_shard_exception","reason":"second failure"}}]}`,
			expectedError:   []string{"failed on 2 shards", "shard=1", "first failure"},
			unexpectedError: "second failure",
		},
	}
	for _, tt := range tests {
		for _, operation := range []string{"query", "values"} {
			t.Run(tt.name+"/"+operation, func(t *testing.T) {
				body := fmt.Sprintf(`{"timed_out":%t,"_shards":%s,"hits":{"hits":[{"_id":"one","_source":{}}]},"aggregations":{"sterms#terms":{"buckets":[{"key":"node-a","doc_count":1}]}}}`, tt.isTimedOut, tt.shards)
				backend := newResponseBackend(t, http.StatusOK, body)
				var err error
				var count int
				if operation == "query" {
					var records []driver.Record
					records, err = queryRecords(t.Context(), backend, driver.Query{Limit: 1})
					count = len(records)
					if err != nil && records != nil {
						t.Fatalf("Query() returned partial records: %v", records)
					}
				} else {
					var values []string
					values, err = backend.Values(t.Context(), "label", driver.Query{}, 10)
					count = len(values)
					if err != nil && values != nil {
						t.Fatalf("Values() returned partial values: %v", values)
					}
				}
				if len(tt.expectedError) == 0 {
					if err != nil || count != 1 {
						t.Fatalf("%s returned count=%d, error=%v, want (1, nil)", operation, count, err)
					}
					return
				}
				for _, detail := range tt.expectedError {
					if err == nil || !strings.Contains(err.Error(), detail) {
						t.Errorf("%s error = %v, want %q", operation, err, detail)
					}
				}
				if tt.unexpectedError != "" && err != nil && strings.Contains(err.Error(), tt.unexpectedError) {
					t.Errorf("%s error = %v, unexpectedly includes %q", operation, err, tt.unexpectedError)
				}
			})
		}
	}
}

func TestStorageCountShardFailure(t *testing.T) {
	backend := newResponseBackend(t, http.StatusOK,
		`{"count":12,"_shards":{"total":2,"successful":1,"failed":1,"failures":[{"shard":3,"reason":{"type":"query_shard_exception","reason":"invalid range"}}]}}`)
	count, err := backend.Count(t.Context(), driver.Query{})
	if count != 0 {
		t.Fatalf("Count() = %d, want 0", count)
	}
	for _, detail := range []string{"count profiles", "failed on 1 shards", "shard=3", "query_shard_exception", "invalid range"} {
		if err == nil || !strings.Contains(err.Error(), detail) {
			t.Errorf("Count() error = %v, want %q", err, detail)
		}
	}
}

func TestStorageDeleteByQueryStatus(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		expectedError string
	}{
		{name: "optional fields absent", body: `{"deleted":2}`},
		{name: "optional fields null", body: `{"deleted":2,"timed_out":null,"version_conflicts":null}`},
		{name: "complete", body: `{"deleted":2,"timed_out":false,"failures":[],"version_conflicts":0}`},
		{
			name:          "timeout",
			body:          `{"deleted":2,"timed_out":true}`,
			expectedError: "timed out",
		},
		{
			name: "version conflicts without failures",
			body: `{"deleted":2,"version_conflicts":3}`,
		},
		{
			name:          "timeout with failures",
			body:          `{"deleted":2,"timed_out":true,"failures":[{"index":"profiles","id":"three","status":429,"cause":{"type":"es_rejected_execution_exception","reason":"queue full"}}]}`,
			expectedError: "timed out",
		},
		{
			name:          "failures and version conflicts",
			body:          `{"deleted":2,"version_conflicts":1,"failures":[{"index":"profiles","id":"three","status":409,"cause":{"type":"version_conflict_engine_exception","reason":"version changed"}}]}`,
			expectedError: "returned 1 failures",
		},
		{
			name:          "multiple failures",
			body:          `{"deleted":2,"failures":[{"index":"profiles","id":"three","status":429,"cause":{"type":"es_rejected_execution_exception"}},{"index":"profiles","id":"four","status":429,"cause":{"type":"es_rejected_execution_exception"}}]}`,
			expectedError: "returned 2 failures",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := newResponseBackend(t, http.StatusOK, tt.body)
			deleted, err := backend.DeleteByQuery(t.Context(), driver.DeleteQuery{
				Filters: []driver.Filter{{Field: "label", Op: driver.OpEq, Value: "node-a"}},
			})
			if deleted != 2 {
				t.Fatalf("DeleteByQuery() deleted=%d, want 2", deleted)
			}
			if tt.expectedError == "" {
				if err != nil {
					t.Fatalf("DeleteByQuery() error = %v", err)
				}
				return
			}
			expected := "elasticsearch backend delete by query profiles " + tt.expectedError
			if err == nil || err.Error() != expected {
				t.Errorf("DeleteByQuery() error = %v, want %q", err, expected)
			}
		})
	}
}

func TestStorageWriteResponse(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		expectedError string
		expectedLog   string
	}{
		{name: "complete", body: `{"_shards":{"total":1,"successful":1,"failed":0}}`},
		{
			name:        "replica failure",
			body:        `{"_shards":{"total":2,"successful":1,"failed":1,"failures":[{"index":"profiles","shard":0,"reason":{"type":"unavailable_shards_exception","reason":"replica unavailable"}}]}}`,
			expectedLog: "replica unavailable",
		},
		{name: "invalid json", body: `{"_shards":`, expectedError: "decode:"},
	}
	for _, tt := range tests {
		for _, operation := range []string{"save", "delete"} {
			t.Run(tt.name+"/"+operation, func(t *testing.T) {
				var logs bytes.Buffer
				log.SetOutput(&logs)
				t.Cleanup(func() { log.SetOutput(os.Stdout) })
				backend := newResponseBackend(t, http.StatusOK, tt.body)
				var err error
				if operation == "save" {
					err = backend.Save(t.Context(), driver.Record{ID: "one", Data: []byte(`{}`)}, driver.SaveOptions{WaitForVisibility: true})
				} else {
					err = backend.Delete(t.Context(), "one")
				}
				if tt.expectedError != "" {
					if err == nil || !strings.Contains(err.Error(), tt.expectedError) {
						t.Fatalf("%s error = %v, want %q", operation, err, tt.expectedError)
					}
					return
				}
				if err != nil {
					t.Fatalf("%s error = %v", operation, err)
				}
				if tt.expectedLog == "" {
					if logs.Len() != 0 {
						t.Fatalf("unexpected logs: %s", logs.String())
					}
					return
				}
				for _, detail := range []string{tt.expectedLog, `level="warning"`, `index="profiles"`, `id="one"`} {
					if !strings.Contains(logs.String(), detail) {
						t.Errorf("logs = %s, want %q", logs.String(), detail)
					}
				}
			})
		}
	}
}

func TestStorageDeleteByQueryHTTPFailure(t *testing.T) {
	backend := newResponseBackend(t, http.StatusConflict,
		`{"deleted":2,"version_conflicts":1,"failures":[{"index":"profiles","id":"three","status":409,"cause":{"type":"version_conflict_engine_exception","reason":"version changed"}}]}`)
	deleted, err := backend.DeleteByQuery(t.Context(), driver.DeleteQuery{
		Filters: []driver.Filter{{Field: "label", Op: driver.OpEq, Value: "node-a"}},
	})
	if deleted != 0 {
		t.Fatalf("DeleteByQuery() deleted=%d, want 0", deleted)
	}
	for _, detail := range []string{"status 409", "version_conflict_engine_exception", "version changed"} {
		if err == nil || !strings.Contains(err.Error(), detail) {
			t.Errorf("DeleteByQuery() error = %v, want %q", err, detail)
		}
	}
}

func TestStorageBulkReplicaFailure(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stdout) })
	backend := newResponseBackend(t, http.StatusOK,
		`{"errors":false,"items":[{"index":{"_index":"profiles","_id":"one","status":201,"_shards":{"total":2,"successful":1,"failed":1}}}]}`)
	if err := backend.Save(t.Context(), driver.Record{ID: "one", Data: []byte(`{}`)}, driver.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := backend.bulk.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	backend.bulk = nil

	for _, detail := range []string{"bulk save replica failure", `level="warning"`, `index="profiles"`, `id="one"`, `failed_shards="1"`} {
		if !strings.Contains(logs.String(), detail) {
			t.Errorf("logs = %s, want %q", logs.String(), detail)
		}
	}
}

func TestResponseError(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		expected []string
	}{
		{
			name:     "structured causes",
			body:     `{"status":500,"error":{"type":"search_phase_execution_exception","reason":"all shards failed","root_cause":[{"type":"query_shard_exception","reason":"invalid range"}],"caused_by":{"type":"number_format_exception","reason":"not a number"}}}`,
			expected: []string{"status 400", "search_phase_execution_exception", "all shards failed", "root_cause", "invalid range", "caused_by", "not a number"},
		},
		{name: "string error", body: `{"error":"request rejected"}`, expected: []string{"request rejected"}},
		{name: "missing reason", body: `{"error":{"type":"security_exception"}}`, expected: []string{"security_exception"}},
		{name: "proxy error", body: `<html>bad gateway</html>`, expected: []string{"<html>bad gateway</html>"}},
		{name: "unrecognized json", body: `{"message":"request rejected"}`, expected: []string{`{"message":"request rejected"}`}},
		{name: "empty body", expected: []string{"status 400"}},
		{name: "large body", body: strings.Repeat("x", 8192) + "END", expected: []string{"truncated"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := &esapi.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader(tt.body))}
			defer res.Body.Close()
			err := responseError("query documents", "profiles", res)
			for _, detail := range append(tt.expected, "query documents profiles") {
				if !strings.Contains(err.Error(), detail) {
					t.Errorf("responseError() = %v, want %q", err, detail)
				}
			}
			if tt.name == "large body" && (len(err.Error()) > 4300 || strings.Contains(err.Error(), "END")) {
				t.Errorf("responseError() did not bound the body: %d bytes", len(err.Error()))
			}
		})
	}
}

func TestResponseErrorReadFailure(t *testing.T) {
	readError := errors.New("connection reset")
	res := &esapi.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(iotest.ErrReader(readError))}
	defer res.Body.Close()
	err := responseError("query documents", "profiles", res)
	if !errors.Is(err, readError) || !strings.Contains(err.Error(), "status 502") {
		t.Fatalf("responseError() = %v, want wrapped read error and HTTP status", err)
	}
}

type benchmarkBulkIndexer struct {
	item esutil.BulkIndexerItem
}

//nolint:gocritic // The SDK's BulkIndexer interface passes items by value.
func (b *benchmarkBulkIndexer) Add(ctx context.Context, item esutil.BulkIndexerItem) error {
	b.item = item
	if item.OnSuccess != nil {
		item.OnSuccess(ctx, item, esutil.BulkIndexerResponseItem{})
	}

	return nil
}

func (*benchmarkBulkIndexer) Close(context.Context) error {
	return nil
}

func (*benchmarkBulkIndexer) Stats() esutil.BulkIndexerStats {
	return esutil.BulkIndexerStats{}
}

func BenchmarkStorageSave(b *testing.B) {
	// Isolate item construction and the successful callback from batching and I/O.
	indexer := &benchmarkBulkIndexer{}
	backend := Storage{index: "profiles", bulk: indexer}
	record := driver.Record{ID: "one", Data: []byte(`{"label":"node-a"}`)}
	b.ReportAllocs()
	for b.Loop() {
		if err := backend.Save(b.Context(), record, driver.SaveOptions{}); err != nil {
			b.Fatal(err)
		}
	}
	if indexer.item.DocumentID != record.ID {
		b.Fatalf("queued document ID = %q, want %q", indexer.item.DocumentID, record.ID)
	}
}
