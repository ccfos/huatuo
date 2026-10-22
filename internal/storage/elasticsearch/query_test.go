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
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/storage/driver"
)

type scrollTestState struct {
	pages   atomic.Int64
	cleared atomic.Int64
	total   int
	failure string
}

func newScrollBackend(t testing.TB, total int, failure string) (*Storage, *scrollTestState) {
	t.Helper()
	state := &scrollTestState{total: total, failure: failure}
	idPrefix := "page-"
	if failure == "long ID" {
		idPrefix = strings.Repeat("a/+", 1500) + idPrefix
	}
	position, size := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte(`{"version":{"number":"8.15.5"}}`))
			return
		}
		if r.Method == http.MethodDelete {
			var body struct {
				IDs []string `json:"scroll_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if len(body.IDs) != 1 {
				t.Error("clear scroll requires the latest ID")
				return
			}
			id := strings.TrimPrefix(body.IDs[0], idPrefix)
			page, err := strconv.ParseInt(id, 10, 64)
			if err != nil {
				t.Errorf("clear scroll ID: %v", err)
			}
			state.cleared.Store(page)
			switch state.failure {
			case "clear rejected":
				_, _ = w.Write([]byte(`{"succeeded":false,"num_freed":1}`))
				return
			case "clear invalid json":
				_, _ = w.Write([]byte(`{"succeeded":`))
				return
			case "clear http":
				http.Error(w, `{"error":{"type":"unavailable","reason":"cleanup unavailable"}}`, http.StatusServiceUnavailable)
				return
			case "clear transport":
				panic(http.ErrAbortHandler)
			case "clear empty":
				_, _ = w.Write([]byte(`{"succeeded":true,"num_freed":0}`))
				return
			}
			_, _ = w.Write([]byte(`{"succeeded":true,"num_freed":1}`))
			return
		}
		if r.URL.Path == "/profiles/_search" {
			position = 0
			var body struct {
				Size           int  `json:"size"`
				From           *int `json:"from"`
				TrackTotalHits bool `json:"track_total_hits"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.From != nil || !body.TrackTotalHits || r.URL.Query().Get("scroll") == "" {
				t.Errorf("search must use scroll without from: %s", r.URL)
			}
			size = body.Size
		} else if r.URL.Path == "/_search/scroll" {
			if r.URL.Query().Has("scroll_id") || r.URL.Query().Get("scroll") == "" {
				t.Error("scroll must keep its ID in the body and renew its lifetime")
			}
			var body struct {
				ID string `json:"scroll_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			want := idPrefix + strconv.FormatInt(state.pages.Load(), 10)
			if body.ID != want {
				t.Errorf("scroll ID=%q, want %q", body.ID, want)
			}
		} else {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		page := state.pages.Add(1)
		if page == 1 && state.failure == "missing index" {
			http.Error(w, `{"error":{"type":"index_not_found_exception","reason":"missing index"}}`, http.StatusNotFound)
			return
		}
		if page == 2 && state.failure == "http" {
			http.Error(w, `{"error":{"type":"unavailable","reason":"injected failure"}}`, http.StatusServiceUnavailable)
			return
		}
		if page == 2 && state.failure == "expired" {
			http.Error(w, `{"error":{"type":"search_context_missing_exception","reason":"expired cursor"}}`, http.StatusNotFound)
			return
		}
		hits := make([]map[string]any, 0, size)
		for len(hits) < size && position < state.total {
			hits = append(hits, map[string]any{"_id": strconv.Itoa(position), "_source": map[string]any{"timestamp": "2026-10-08T00:00:00Z"}})
			position++
		}
		payload := map[string]any{
			"_scroll_id": idPrefix + strconv.FormatInt(page, 10),
			"timed_out":  page == 2 && state.failure == "timeout",
			"_shards":    map[string]any{"failed": 0},
			"hits":       map[string]any{"hits": hits},
		}
		if page == 2 && state.failure == "shards" {
			payload["_shards"] = map[string]any{"failed": 1}
		}
		if page == 2 && state.failure == "missing ID" {
			delete(payload, "_scroll_id")
		}
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	backend, err := NewBackend(&Config{Addresses: []string{server.URL}, Index: "profiles"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := backend.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return backend, state
}

func BenchmarkQuery(b *testing.B) {
	for _, tc := range []struct {
		name  string
		query driver.Query
	}{
		{name: "batches", query: driver.Query{Limit: 1000, BatchSize: 100}},
		{name: "deep offset", query: driver.Query{Limit: 1, Offset: 10000, BatchSize: 100}},
	} {
		b.Run(tc.name, func(b *testing.B) {
			backend, state := newScrollBackend(b, 11000, "")
			b.ReportAllocs()
			for b.Loop() {
				if err := backend.Query(b.Context(), tc.query, func([]driver.Record) error { return nil }); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(state.pages.Load())/float64(b.N), "requests/op")
		})
	}
}

func TestQueryBatches(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		total, limit, offset, size int
		pages                      int64
	}{
		{name: "partial last batch", total: 20, limit: 7, offset: 3, size: 4, pages: 3},
		{name: "default batch", total: 250, limit: 201, pages: 3},
		{name: "larger than result window", total: 11050, limit: 10501, offset: 7, size: 100, pages: 106},
		{name: "deep offset", total: 11050, limit: 7, offset: 10001, size: 100, pages: 101},
		{name: "one after deep offset", total: 11050, limit: 1, offset: 10000, size: 100, pages: 101},
		{name: "default batch after deep offset", total: 11050, limit: 1, offset: 10000, pages: 101},
		{name: "offset and limit within batch", total: 20, limit: 1, offset: 2, size: 100, pages: 1},
		{name: "exhausted", total: 7, limit: 20, offset: 3, size: 4, pages: 3},
		{name: "offset beyond end", total: 7, limit: 20, offset: 10, size: 4, pages: 3},
		{name: "maximum offset", total: 7, limit: 1, offset: math.MaxInt, size: 4, pages: 3},
		{name: "maximum limit", total: 7, limit: math.MaxInt, offset: 3, size: 4, pages: 3},
		{name: "empty", limit: 20, size: 4, pages: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend, state := newScrollBackend(t, tc.total, "")
			q := driver.Query{Limit: tc.limit, Offset: tc.offset, BatchSize: tc.size}
			var batches [][]driver.Record
			err := backend.Query(t.Context(), q, func(batch []driver.Record) error {
				if len(batch) == 0 || len(batch) > q.ReadSize() {
					t.Errorf("batch size=%d", len(batch))
				}
				batches = append(batches, batch)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, batch := range batches {
				for i := range batch {
					if want := strconv.Itoa(tc.offset + count); batch[i].ID != want {
						t.Fatalf("record=%q, want %q", batch[i].ID, want)
					}
					count++
				}
			}
			if want := min(tc.limit, max(0, tc.total-tc.offset)); count != want {
				t.Fatalf("count=%d, want %d", count, want)
			}
			if state.cleared.Load() != state.pages.Load() {
				t.Fatalf("cleared page=%d, last page=%d", state.cleared.Load(), state.pages.Load())
			}
			if got := state.pages.Load(); got != tc.pages {
				t.Fatalf("requests=%d, want %d", got, tc.pages)
			}
		})
	}
}

func TestQueryLongScrollID(t *testing.T) {
	backend, state := newScrollBackend(t, 3, "long ID")
	var ids []string
	err := backend.Query(t.Context(), driver.Query{Limit: 3, BatchSize: 1}, func(records []driver.Record) error {
		for i := range records {
			ids = append(ids, records[i].ID)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ids, ","); got != "0,1,2" {
		t.Fatalf("IDs=%q, want 0,1,2", got)
	}
	if state.pages.Load() != 3 || state.cleared.Load() != 3 {
		t.Fatalf("pages=%d cleared=%d, want 3 each", state.pages.Load(), state.cleared.Load())
	}
}

func TestQueryRecordOwnership(t *testing.T) {
	backend, _ := newScrollBackend(t, 6, "")
	var batches [][]driver.Record
	err := backend.Query(t.Context(), driver.Query{Limit: 6, BatchSize: 2}, func(records []driver.Record) error {
		for i := range records {
			if !json.Valid(records[i].Data) {
				t.Fatalf("record %s shares previously modified data: %q", records[i].ID, records[i].Data)
			}
		}
		// A consumer may retain and mutate one record without affecting other records or pages.
		records[0].Data[0] = byte('a' + len(batches))
		batches = append(batches, records)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 3 {
		t.Fatalf("batches=%d, want 3", len(batches))
	}
	for i, batch := range batches {
		if batch[0].Data[0] != byte('a'+i) || !json.Valid(batch[1].Data) {
			t.Fatalf("batch %d data changed: %q, %q", i, batch[0].Data, batch[1].Data)
		}
	}
}

func TestQueryMissingIndex(t *testing.T) {
	backend, state := newScrollBackend(t, 1, "missing index")
	err := backend.Query(t.Context(), driver.Query{Limit: 1}, func([]driver.Record) error {
		t.Fatal("missing index must not produce records")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.pages.Load() != 1 || state.cleared.Load() != 0 {
		t.Fatalf("pages=%d cleared=%d, want 1 and 0", state.pages.Load(), state.cleared.Load())
	}
}

func TestQueryCleanup(t *testing.T) {
	consumerErr := errors.New("consumer failed")
	for _, tc := range []struct {
		name string
		log  string
	}{
		{name: "success"},
		{name: "clear empty"},
		{name: "clear rejected", log: "succeeded=false"},
		{name: "clear invalid json", log: "failed to decode Elasticsearch clear scroll response"},
		{name: "clear http", log: "cleanup unavailable"},
		{name: "clear transport", log: "failed to clear Elasticsearch query scroll"},
	} {
		for _, result := range []struct {
			name string
			err  error
		}{
			{name: "query success"},
			{name: "consumer failure", err: consumerErr},
		} {
			t.Run(tc.name+"/"+result.name, func(t *testing.T) {
				var logs bytes.Buffer
				log.SetOutput(&logs)
				t.Cleanup(func() { log.SetOutput(os.Stdout) })
				backend, state := newScrollBackend(t, 1, tc.name)
				err := backend.Query(t.Context(), driver.Query{Limit: 1}, func([]driver.Record) error {
					return result.err
				})
				if !errors.Is(err, result.err) {
					t.Fatalf("query error=%v, want %v", err, result.err)
				}
				if state.pages.Load() != 1 || state.cleared.Load() != 1 {
					t.Fatalf("pages=%d cleared=%d, want 1 each", state.pages.Load(), state.cleared.Load())
				}
				if tc.log == "" {
					if logs.Len() != 0 {
						t.Fatalf("unexpected cleanup warning: %s", &logs)
					}
					return
				}
				if !strings.Contains(logs.String(), tc.log) || !strings.Contains(logs.String(), `level="warning"`) {
					t.Fatalf("cleanup log=%s, want warning containing %q", &logs, tc.log)
				}
			})
		}
	}
}

func TestQueryStopsOnFailure(t *testing.T) {
	for _, failure := range []string{"http", "expired", "timeout", "shards", "missing ID", "consumer", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			backend, state := newScrollBackend(t, 10, failure)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			consumerErr := errors.New("consumer failed")
			calls := 0
			err := backend.Query(ctx, driver.Query{Limit: 10, BatchSize: 2}, func([]driver.Record) error {
				calls++
				if failure == "consumer" {
					return consumerErr
				}
				if failure == "cancel" {
					cancel()
				}
				return nil
			})
			if err == nil {
				t.Fatal("query succeeded after injected failure")
			}
			if failure == "consumer" && !errors.Is(err, consumerErr) {
				t.Fatalf("consumer error lost: %v", err)
			}
			if failure == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			wantPages, wantCalls, wantCleared := int64(2), 1, int64(2)
			switch failure {
			case "consumer", "cancel":
				wantPages, wantCleared = 1, 1
			case "http", "expired":
				wantCleared = 1
			case "missing ID":
				wantCalls, wantCleared = 2, 1
			}
			if state.pages.Load() != wantPages || calls != wantCalls || state.cleared.Load() != wantCleared {
				t.Fatalf("pages=%d calls=%d cleared=%d, want %d %d %d", state.pages.Load(), calls, state.cleared.Load(), wantPages, wantCalls, wantCleared)
			}
		})
	}
}
