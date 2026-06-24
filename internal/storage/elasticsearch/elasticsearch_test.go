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
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"huatuo-bamai/internal/storage/driver"
)

type mockElasticsearchDocument struct {
	ID     string
	Source json.RawMessage
	Fields map[string]any
}

type mockElasticsearchServer struct {
	mu                sync.Mutex
	indexes           map[string]map[string]mockElasticsearchDocument
	createIndexBodies []map[string]any
	searchBodies      []map[string]any
	countBodies       []map[string]any
	server            *httptest.Server
}

func newMockElasticsearchServer() *mockElasticsearchServer {
	mockServer := &mockElasticsearchServer{
		indexes: make(map[string]map[string]mockElasticsearchDocument),
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Elastic-Product", "Elasticsearch")

		path := strings.Trim(r.URL.Path, "/")
		if path == "" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"name":"mock-es","version":{"number":"7.17.0"}}`))
			return
		}

		parts := strings.Split(path, "/")
		switch {
		case len(parts) == 1 && parts[0] == "_bulk":
			mockServer.handleBulk(w, r, "")
		case len(parts) == 2 && parts[1] == "_bulk":
			mockServer.handleBulk(w, r, parts[0])
		case len(parts) == 1 && r.Method == http.MethodHead:
			mockServer.handleIndexExists(w, parts[0])
		case len(parts) == 1 && r.Method == http.MethodPut:
			mockServer.handleCreateIndex(w, r, parts[0])
		case len(parts) == 3 && parts[1] == "_doc" && (r.Method == http.MethodPut || r.Method == http.MethodPost):
			mockServer.handleSaveDocument(w, r, parts[0], parts[2])
		case len(parts) == 3 && parts[1] == "_doc" && r.Method == http.MethodGet:
			mockServer.handleGetDocument(w, parts[0], parts[2])
		case len(parts) == 3 && parts[1] == "_doc" && r.Method == http.MethodDelete:
			mockServer.handleDeleteDocument(w, parts[0], parts[2])
		case len(parts) == 2 && parts[1] == "_search":
			mockServer.handleSearch(w, r, parts[0])
		case len(parts) == 2 && parts[1] == "_count":
			mockServer.handleCount(w, r, parts[0])
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"route not found"}`))
		}
	})

	mockServer.server = httptest.NewServer(handler)
	return mockServer
}

func (m *mockElasticsearchServer) Close() {
	if m == nil || m.server == nil {
		return
	}
	m.server.Close()
}

func (m *mockElasticsearchServer) URL() string {
	if m == nil || m.server == nil {
		return ""
	}
	return m.server.URL
}

func (m *mockElasticsearchServer) handleIndexExists(w http.ResponseWriter, index string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.indexes[index]; ok {
		w.WriteHeader(http.StatusOK)
		return
	}

	w.WriteHeader(http.StatusNotFound)
}

func (m *mockElasticsearchServer) handleCreateIndex(w http.ResponseWriter, r *http.Request, index string) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.indexes[index]; !ok {
		m.indexes[index] = make(map[string]mockElasticsearchDocument)
	}
	m.createIndexBodies = append(m.createIndexBodies, body)

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"acknowledged":true}`))
}

func (m *mockElasticsearchServer) handleSaveDocument(w http.ResponseWriter, r *http.Request, index, id string) {
	var raw json.RawMessage
	_ = json.NewDecoder(r.Body).Decode(&raw)

	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.indexes[index]; !ok {
		m.indexes[index] = make(map[string]mockElasticsearchDocument)
	}
	m.indexes[index][id] = mockElasticsearchDocument{
		ID:     id,
		Source: cloneRawMessage(raw),
		Fields: fields,
	}

	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write([]byte(`{"result":"created"}`))
}

// handleBulk consumes NDJSON bulk requests. Only `index` actions are supported
// since that is all the production Save path emits. Each accepted item is
// stored under the index from either the action line or the URL path.
func (m *mockElasticsearchServer) handleBulk(w http.ResponseWriter, r *http.Request, defaultIndex string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	lines := bytes.Split(bytes.TrimRight(body, "\n"), []byte("\n"))

	type bulkAction struct {
		Index struct {
			Index string `json:"_index"`
			ID    string `json:"_id"`
		} `json:"index"`
	}

	items := make([]map[string]any, 0, len(lines)/2)

	m.mu.Lock()
	defer m.mu.Unlock()

	for i := 0; i+1 < len(lines); i += 2 {
		var act bulkAction
		if err := json.Unmarshal(lines[i], &act); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		idx := act.Index.Index
		if idx == "" {
			idx = defaultIndex
		}
		id := act.Index.ID

		source := cloneRawMessage(lines[i+1])
		var fields map[string]any
		_ = json.Unmarshal(source, &fields)

		if _, ok := m.indexes[idx]; !ok {
			m.indexes[idx] = make(map[string]mockElasticsearchDocument)
		}
		m.indexes[idx][id] = mockElasticsearchDocument{ID: id, Source: source, Fields: fields}

		items = append(items, map[string]any{
			"index": map[string]any{
				"_index":   idx,
				"_id":      id,
				"_version": 1,
				"result":   "created",
				"status":   201,
			},
		})
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"took":   1,
		"errors": false,
		"items":  items,
	})
}

func (m *mockElasticsearchServer) handleGetDocument(w http.ResponseWriter, index, id string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	docs, ok := m.indexes[index]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"found":false}`))
		return
	}

	doc, ok := docs[id]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"found":false}`))
		return
	}

	resp := map[string]any{
		"_id":     id,
		"found":   true,
		"_source": doc.Source,
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (m *mockElasticsearchServer) handleDeleteDocument(w http.ResponseWriter, index, id string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if docs, ok := m.indexes[index]; ok {
		if _, found := docs[id]; found {
			delete(docs, id)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":"deleted"}`))
			return
		}
	}

	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"result":"not_found"}`))
}

func (m *mockElasticsearchServer) handleSearch(w http.ResponseWriter, r *http.Request, index string) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)

	m.mu.Lock()
	defer m.mu.Unlock()

	m.searchBodies = append(m.searchBodies, body)
	if body["aggs"] != nil {
		docs := m.matchDocumentsLocked(index, body["query"])
		m.handleTermsSearch(w, body, docs)
		return
	}
	docs := m.queryDocumentsLocked(index, body)

	hits := make([]map[string]any, 0, len(docs))
	for _, doc := range docs {
		hits = append(hits, map[string]any{
			"_id":     doc.ID,
			"_source": doc.Source,
		})
	}

	resp := map[string]any{
		"hits": map[string]any{
			"total": map[string]any{
				"value":    len(docs),
				"relation": "eq",
			},
			"hits": hits,
		},
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (m *mockElasticsearchServer) handleTermsSearch(w http.ResponseWriter, body map[string]any, docs []mockElasticsearchDocument) {
	aggs, _ := body["aggs"].(map[string]any)
	termsAggregation, _ := aggs["terms"].(map[string]any)
	termsConfig, _ := termsAggregation["terms"].(map[string]any)
	fieldName := stringValue(termsConfig["field"])

	counts := make(map[string]int)
	for _, doc := range docs {
		key := stringValue(doc.Fields[fieldName])
		if key == "" {
			continue
		}
		counts[key]++
	}

	type bucket struct {
		Key   string
		Count int
	}

	buckets := make([]bucket, 0, len(counts))
	for key, count := range counts {
		buckets = append(buckets, bucket{Key: key, Count: count})
	}

	sort.SliceStable(buckets, func(i, j int) bool {
		if buckets[i].Count == buckets[j].Count {
			return buckets[i].Key < buckets[j].Key
		}
		return buckets[i].Count > buckets[j].Count
	})

	responseBuckets := make([]map[string]any, 0, len(buckets))
	for _, item := range buckets {
		responseBuckets = append(responseBuckets, map[string]any{
			"key":       item.Key,
			"doc_count": item.Count,
		})
	}

	resp := map[string]any{
		"hits": map[string]any{
			"total": map[string]any{
				"value":    len(docs),
				"relation": "eq",
			},
			"hits": []any{},
		},
		"aggregations": map[string]any{
			"terms": map[string]any{
				"buckets": responseBuckets,
			},
		},
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (m *mockElasticsearchServer) handleCount(w http.ResponseWriter, r *http.Request, index string) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)

	m.mu.Lock()
	defer m.mu.Unlock()

	m.countBodies = append(m.countBodies, body)
	docs := m.queryDocumentsLocked(index, body)

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"count": len(docs)})
}

func (m *mockElasticsearchServer) queryDocumentsLocked(index string, body map[string]any) []mockElasticsearchDocument {
	docs := m.matchDocumentsLocked(index, body["query"])

	applySorts(docs, body["sort"])

	from := intFromAny(body["from"])
	if from > len(docs) {
		return []mockElasticsearchDocument{}
	}

	size := len(docs)
	if rawSize, ok := body["size"]; ok {
		size = intFromAny(rawSize)
	}
	if size < 0 {
		size = 0
	}

	end := len(docs)
	if from+size < end {
		end = from + size
	}
	if from > end {
		return []mockElasticsearchDocument{}
	}

	return append([]mockElasticsearchDocument(nil), docs[from:end]...)
}

func (m *mockElasticsearchServer) matchDocumentsLocked(index string, rawQuery any) []mockElasticsearchDocument {
	docsByID := m.indexes[index]
	docs := make([]mockElasticsearchDocument, 0, len(docsByID))
	for _, doc := range docsByID {
		if matchesQuery(doc, rawQuery) {
			docs = append(docs, doc)
		}
	}
	return docs
}

func matchesQuery(doc mockElasticsearchDocument, rawQuery any) bool {
	queryMap, ok := rawQuery.(map[string]any)
	if !ok || len(queryMap) == 0 {
		return true
	}
	if _, ok := queryMap["match_all"]; ok {
		return true
	}

	boolQuery, ok := queryMap["bool"].(map[string]any)
	if !ok {
		return true
	}

	for _, clause := range toAnySlice(boolQuery["filter"]) {
		if !matchesClause(doc, clause) {
			return false
		}
	}
	for _, clause := range toAnySlice(boolQuery["must_not"]) {
		if matchesClause(doc, clause) {
			return false
		}
	}

	return true
}

func matchesClause(doc mockElasticsearchDocument, rawClause any) bool {
	clause, ok := rawClause.(map[string]any)
	if !ok {
		return false
	}

	if rawTerm, ok := clause["term"].(map[string]any); ok {
		for path, rawValue := range rawTerm {
			// Handle both short form {"field": "val"} and long form {"field": {"value": "val"}}.
			var value any
			if m, ok := rawValue.(map[string]any); ok {
				value = m["value"]
			} else {
				value = rawValue
			}
			return valuesEqual(fieldValue(doc, path), value)
		}
	}

	if rawTerms, ok := clause["terms"].(map[string]any); ok {
		for path, values := range rawTerms {
			current := fieldValue(doc, path)
			for _, value := range toAnySlice(values) {
				if valuesEqual(current, value) {
					return true
				}
			}
			return false
		}
	}

	if rawRange, ok := clause["range"].(map[string]any); ok {
		for path, conditions := range rawRange {
			conditionMap, ok := conditions.(map[string]any)
			if !ok {
				return false
			}
			current := fieldValue(doc, path)
			for operator, value := range conditionMap {
				compareResult, comparable := compareValues(current, value)
				if !comparable {
					return false
				}
				switch operator {
				case "gt":
					if compareResult <= 0 {
						return false
					}
				case "gte":
					if compareResult < 0 {
						return false
					}
				case "lt":
					if compareResult >= 0 {
						return false
					}
				case "lte":
					if compareResult > 0 {
						return false
					}
				default:
					return false
				}
			}
			return true
		}
	}

	return false
}

func applySorts(docs []mockElasticsearchDocument, rawSort any) {
	sortClauses := toAnySlice(rawSort)
	if len(sortClauses) == 0 {
		sort.SliceStable(docs, func(i, j int) bool {
			return docs[i].ID < docs[j].ID
		})
		return
	}

	sort.SliceStable(docs, func(i, j int) bool {
		left := docs[i]
		right := docs[j]

		for _, rawClause := range sortClauses {
			clause, ok := rawClause.(map[string]any)
			if !ok {
				continue
			}
			for path, rawOptions := range clause {
				options, _ := rawOptions.(map[string]any)
				desc := strings.EqualFold(stringValue(options["order"]), "desc")
				compareResult, comparable := compareValues(fieldValue(left, path), fieldValue(right, path))
				if !comparable || compareResult == 0 {
					continue
				}
				if desc {
					return compareResult > 0
				}
				return compareResult < 0
			}
		}

		return left.ID < right.ID
	})
}

func fieldValue(doc mockElasticsearchDocument, path string) any {
	return doc.Fields[path]
}

func valuesEqual(left, right any) bool {
	if leftFloat, ok := toFloat64(left); ok {
		rightFloat, ok := toFloat64(right)
		if !ok {
			return false
		}
		return leftFloat == rightFloat
	}
	return stringValue(left) == stringValue(right)
}

func compareValues(left, right any) (int, bool) {
	if leftFloat, ok := toFloat64(left); ok {
		rightFloat, ok := toFloat64(right)
		if !ok {
			return 0, false
		}
		switch {
		case leftFloat < rightFloat:
			return -1, true
		case leftFloat > rightFloat:
			return 1, true
		default:
			return 0, true
		}
	}

	leftString := stringValue(left)
	rightString := stringValue(right)
	switch {
	case leftString < rightString:
		return -1, true
	case leftString > rightString:
		return 1, true
	default:
		return 0, true
	}
}

func toFloat64(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	default:
		return 0, false
	}
}

func toAnySlice(value any) []any {
	switch typed := value.(type) {
	case []any:
		return typed
	case []map[string]any:
		result := make([]any, 0, len(typed))
		for _, item := range typed {
			result = append(result, item)
		}
		return result
	default:
		return nil
	}
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	default:
		return 0
	}
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.RawMessage:
		return string(typed)
	case nil:
		return ""
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return string(raw)
	}
}

func cloneRawMessage(data json.RawMessage) json.RawMessage {
	if len(data) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), data...)
}

func decodeJSONMap(t *testing.T, raw []byte) map[string]any {
	t.Helper()

	if len(raw) == 0 {
		return nil
	}

	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("json.Unmarshal() returned error: %v", err)
	}
	return result
}

func newBackendForTest(t *testing.T, server *mockElasticsearchServer) *Storage {
	t.Helper()

	backend, err := NewBackend(&Config{
		Addresses: []string{server.URL()},
		Index:     "huatuo_bamai",
	})
	if err != nil {
		t.Fatalf("NewBackend() returned error: %v", err)
	}
	return backend
}

// flushBackend forces the bulk indexer to drain pending items. Save buffers
// asynchronously, so tests must flush before issuing a read that depends on a
// just-saved record. After flushing the indexer is closed; tests that need to
// keep saving must rebuild the backend.
func flushBackend(t *testing.T, backend *Storage) {
	t.Helper()
	if err := backend.Close(t.Context()); err != nil {
		t.Fatalf("flush bulk: %v", err)
	}
}

// TestBuildSearchRequest covers query DSL construction: verifies that equality, not-equal, range, IN, sort, pagination, and invalid pagination are all translated to the correct ES request body.
func TestBuildSearchRequest(t *testing.T) {
	baseTime := time.Date(2026, 4, 9, 8, 0, 0, 123000000, time.UTC)

	cases := []struct {
		name     string
		query    driver.Query
		validate func(*testing.T, map[string]any, error)
	}{
		{
			name: "filters-sorts-and-pagination",
			query: driver.Query{
				Filters: []driver.Filter{
					{Field: "status", Op: driver.OpEq, Value: "running"},
					{Field: "priority", Op: driver.OpGt, Value: 5},
					{Field: "created_at", Op: driver.OpLte, Value: baseTime},
					{Field: "user_id", Op: driver.OpIn, Value: []string{"user-alpha", "user-beta"}},
				},
				Sorts: []driver.Sort{
					{Field: "priority", Desc: true},
				},
				Limit:  2,
				Offset: 1,
			},
			validate: func(t *testing.T, got map[string]any, err error) {
				if err != nil {
					t.Errorf("buildSearchBody() returned error: %v", err)
					return
				}

				if intFromAny(got["size"]) != 2 {
					t.Errorf("size = %v, want 2", got["size"])
				}
				if intFromAny(got["from"]) != 1 {
					t.Errorf("from = %v, want 1", got["from"])
				}

				queryMap, _ := got["query"].(map[string]any)
				boolQuery, _ := queryMap["bool"].(map[string]any)
				filterClauses := toAnySlice(boolQuery["filter"])
				if len(filterClauses) != 4 {
					t.Errorf("filter clause count = %d, want 4", len(filterClauses))
				}

				sortClauses := toAnySlice(got["sort"])
				if len(sortClauses) != 1 {
					t.Errorf("sort clause count = %d, want 1", len(sortClauses))
				}
			},
		},
		{
			name: "not-equal-uses-must-not",
			query: driver.Query{
				Filters: []driver.Filter{
					{Field: "status", Op: driver.OpNe, Value: "completed"},
				},
			},
			validate: func(t *testing.T, got map[string]any, err error) {
				if err != nil {
					t.Errorf("buildSearchBody() returned error: %v", err)
					return
				}

				queryMap, _ := got["query"].(map[string]any)
				boolQuery, _ := queryMap["bool"].(map[string]any)
				mustNotClauses := toAnySlice(boolQuery["must_not"])
				if len(mustNotClauses) != 1 {
					t.Errorf("must_not clause count = %d, want 1", len(mustNotClauses))
				}
			},
		},
		{
			name: "invalid-pagination",
			query: driver.Query{
				Limit: -1,
			},
			validate: func(t *testing.T, got map[string]any, err error) {
				if err == nil {
					t.Errorf("buildSearchBody() error = nil, want error")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rawBody, err := buildSearchRequest(tc.query)
			body := decodeJSONMap(t, rawBody)
			tc.validate(t, body, err)
		})
	}
}

// TestElasticsearchBackendCRUD covers ES backend initialization and basic CRUD: verifies index creation, document save, lookup by ID, delete of existing and missing documents match the unified storage layer contract.
func TestElasticsearchBackendCRUD(t *testing.T) {
	server := newMockElasticsearchServer()
	defer server.Close()

	backend := newBackendForTest(t, server)
	if backend == nil {
		return
	}

	err := backend.Init(t.Context(), "jobs", []driver.Index{
		{Field: "user_id"},
		{Field: "status"},
		{Field: "priority"},
		{Field: "created_at"},
	})
	if err != nil {
		t.Errorf("Init() returned error: %v", err)
		return
	}

	recordTime := time.Date(2026, 4, 9, 10, 0, 0, 0, time.UTC)
	record := driver.Record{
		ID:   "job-es-alpha",
		Data: []byte(`{"id":"job-es-alpha","status":"running"}`),
		Fields: map[string]any{
			"user_id":    "user-alpha",
			"status":     "running",
			"priority":   9,
			"created_at": recordTime,
		},
	}

	if err := backend.Save(t.Context(), record); err != nil {
		t.Errorf("Save() returned error: %v", err)
	}
	flushBackend(t, backend)

	gotRecord, err := backend.Get(t.Context(), "job-es-alpha")
	if err != nil {
		t.Errorf("Get() returned error: %v", err)
	}
	if !bytes.Equal(gotRecord.Data, record.Data) {
		t.Errorf("Get() data = %s, want %s", string(gotRecord.Data), string(record.Data))
	}

	if err := backend.Delete(t.Context(), "job-es-alpha"); err != nil {
		t.Errorf("Delete() returned error: %v", err)
	}

	_, err = backend.Get(t.Context(), "job-es-alpha")
	if !errors.Is(err, driver.ErrNotFound) {
		t.Errorf("Get() after delete = %v, want ErrNotFound", err)
	}

	if err := backend.Delete(t.Context(), "job-es-missing"); err != nil {
		t.Errorf("Delete() for missing id returned error: %v", err)
	}
}

// TestElasticsearchBackendQuery covers ES backend querying and counting: verifies filter, range conditions, sort, pagination, and Count/Query consistency all work correctly.
func TestElasticsearchBackendQuery(t *testing.T) {
	server := newMockElasticsearchServer()
	defer server.Close()

	backend := newBackendForTest(t, server)
	if backend == nil {
		return
	}

	err := backend.Init(t.Context(), "jobs", []driver.Index{
		{Field: "user_id"},
		{Field: "status"},
		{Field: "priority"},
		{Field: "created_at"},
	})
	if err != nil {
		t.Errorf("Init() returned error: %v", err)
		return
	}

	records := []driver.Record{
		{
			ID:   "job-es-alpha",
			Data: []byte(`{"id":"job-es-alpha","user_id":"user-alpha","status":"running","priority":10}`),
		},
		{
			ID:   "job-es-beta",
			Data: []byte(`{"id":"job-es-beta","user_id":"user-beta","status":"running","priority":6}`),
		},
		{
			ID:   "job-es-gamma",
			Data: []byte(`{"id":"job-es-gamma","user_id":"user-gamma","status":"completed","priority":3}`),
		},
	}

	for _, record := range records {
		if err := backend.Save(t.Context(), record); err != nil {
			t.Errorf("Save(%q) returned error: %v", record.ID, err)
		}
	}
	flushBackend(t, backend)

	result, err := backend.Query(t.Context(), driver.Query{
		Filters: []driver.Filter{
			{Field: "status", Op: driver.OpEq, Value: "running"},
			{Field: "priority", Op: driver.OpGt, Value: 5},
		},
		Sorts: []driver.Sort{
			{Field: "priority", Desc: true},
		},
		Limit:  1,
		Offset: 0,
	})
	if err != nil {
		t.Errorf("Query() returned error: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("Query() result length = %d, want 1", len(result))
	}
	if len(result) == 1 && result[0].ID != "job-es-alpha" {
		t.Errorf("Query() first id = %q, want %q", result[0].ID, "job-es-alpha")
	}

	count, err := backend.Count(t.Context(), driver.Query{
		Filters: []driver.Filter{
			{Field: "status", Op: driver.OpEq, Value: "running"},
			{Field: "priority", Op: driver.OpGt, Value: 5},
		},
	})
	if err != nil {
		t.Errorf("Count() returned error: %v", err)
	}
	if count != 2 {
		t.Errorf("Count() = %d, want 2", count)
	}

	server.mu.Lock()
	searchBodies := append([]map[string]any(nil), server.searchBodies...)
	countBodies := append([]map[string]any(nil), server.countBodies...)
	server.mu.Unlock()

	if len(searchBodies) != 1 {
		t.Errorf("search body count = %d, want 1", len(searchBodies))
	}
	if len(countBodies) != 1 {
		t.Errorf("count body count = %d, want 1", len(countBodies))
	}
}

// TestElasticsearchBackendTerms covers ES backend Terms aggregation: verifies the unified filter is applied during field aggregation and returns a deduplicated list of field values.
func TestElasticsearchBackendTerms(t *testing.T) {
	server := newMockElasticsearchServer()
	defer server.Close()

	backend := newBackendForTest(t, server)
	if backend == nil {
		return
	}

	err := backend.Init(t.Context(), "profiles", []driver.Index{
		{Field: "hostname"},
		{Field: "profile_type"},
		{Field: "time"},
	})
	if err != nil {
		t.Errorf("Init() returned error: %v", err)
		return
	}

	baseTime := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)
	records := []driver.Record{
		{
			ID:   "profile-alpha",
			Data: []byte(`{"tracer_id":"profile-alpha","hostname":"huatuo-dev","profile_type":"process_cpu:cpu:nanoseconds:cpu:nanoseconds","time":"2026-04-09 12:00:00.000 +0000"}`),
		},
		{
			ID:   "profile-beta",
			Data: []byte(`{"tracer_id":"profile-beta","hostname":"huatuo-dev","profile_type":"process_mem:alloc_objects:count:space:bytes","time":"2026-04-09 12:02:00.000 +0000"}`),
		},
		{
			ID:   "profile-gamma",
			Data: []byte(`{"tracer_id":"profile-gamma","hostname":"huatuo-dev","profile_type":"process_cpu:cpu:nanoseconds:cpu:nanoseconds","time":"2026-04-09 12:03:00.000 +0000"}`),
		},
	}

	for _, record := range records {
		if err := backend.Save(t.Context(), record); err != nil {
			t.Errorf("Save(%q) returned error: %v", record.ID, err)
		}
	}
	flushBackend(t, backend)

	terms, err := backend.Values(t.Context(), "profile_type", driver.Query{
		Filters: []driver.Filter{
			{Field: "hostname", Op: driver.OpEq, Value: "huatuo-dev"},
			{Field: "time", Op: driver.OpGte, Value: baseTime.Add(-time.Minute)},
		},
	}, 10)
	if err != nil {
		t.Errorf("Terms() returned error: %v", err)
	}

	sort.Strings(terms)
	expectedTerms := []string{
		"process_cpu:cpu:nanoseconds:cpu:nanoseconds",
		"process_mem:alloc_objects:count:space:bytes",
	}
	if len(terms) != len(expectedTerms) {
		t.Errorf("Terms() count=%d, want %d", len(terms), len(expectedTerms))
	}
	for index, expectedTerm := range expectedTerms {
		if index >= len(terms) {
			break
		}
		if terms[index] != expectedTerm {
			t.Errorf("Terms()[%d]=%q, want %q", index, terms[index], expectedTerm)
		}
	}
}

// newMockServerWithoutProductHeader returns an httptest.Server that responds
// without the X-Elastic-Product header; our productHeaderTransport injects it.
func newMockServerWithoutProductHeader() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"name":"mock-opensearch","version":{"number":"2.11.0","distribution":"opensearch"}}`))
	}))
}

// TestNewBackend_WithoutProductHeader verifies that NewBackend succeeds against
// a server that omits X-Elastic-Product; productHeaderTransport injects the
// header so the v8 client's product check passes.
func TestNewBackend_WithoutProductHeader(t *testing.T) {
	server := newMockServerWithoutProductHeader()
	defer server.Close()

	backend, err := NewBackend(&Config{Addresses: []string{server.URL}})
	if err != nil {
		t.Fatalf("NewBackend() returned error: %v", err)
	}
	if backend == nil {
		t.Fatal("NewBackend() returned nil backend")
	}
}

// TestNewBackend_ServerError verifies that NewBackend returns an error when the
// server responds with a non-2xx status on the Info call.
func TestNewBackend_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"simulated failure"}`))
	}))
	defer server.Close()

	_, err := NewBackend(&Config{Addresses: []string{server.URL}})
	if err == nil {
		t.Fatal("NewBackend() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "elasticsearch client info") {
		t.Errorf("error = %q, want to contain \"elasticsearch client info\"", err.Error())
	}
}
