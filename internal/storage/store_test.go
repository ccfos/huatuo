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

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"huatuo-bamai/internal/storage/driver"
)

type testEntity struct {
	ID     string `json:"id"`
	UserID string `json:"user_id"`
	Status string `json:"status"`
	Cost   int64  `json:"cost"`
}

type testMapper struct {
	indexes   []driver.Index
	fields    map[string]any
	id        string
	encodeErr error
	decodeErr error
	fieldsErr error
}

func (m *testMapper) ID(v testEntity) string {
	if m.id != "" {
		return m.id
	}
	return v.ID
}

func (m *testMapper) Encode(v testEntity) ([]byte, error) {
	if m.encodeErr != nil {
		return nil, m.encodeErr
	}
	return json.Marshal(v)
}

func (m *testMapper) Decode(data []byte) (testEntity, error) {
	if m.decodeErr != nil {
		return testEntity{}, m.decodeErr
	}

	var entity testEntity
	err := json.Unmarshal(data, &entity)
	return entity, err
}

func (m *testMapper) Fields(v testEntity) (map[string]any, error) {
	if m.fieldsErr != nil {
		return nil, m.fieldsErr
	}
	if m.fields != nil {
		return m.fields, nil
	}
	return map[string]any{
		"user_id": v.UserID,
		"status":  v.Status,
		"cost":    v.Cost,
	}, nil
}

func (m *testMapper) Indexes() []driver.Index {
	return m.indexes
}

type testBackend struct {
	initErr      error
	saveErr      error
	getErr       error
	deleteErr    error
	queryErr     error
	countErr     error
	valuesErr    error
	getRecord    driver.Record
	queryRecords []driver.Record
	countValue   int64
	valuesValue  []string
	initCalls    int
	saveCalls    int
	deleteCalls  int
	queryCalls   int
	countCalls   int
	valuesCalls  int
	collection   string
	indexes      []driver.Index
	savedRecord  driver.Record
	deletedID    string
	lastQuery    driver.Query
	valuesField  string
	valuesSize   int
}

func (b *testBackend) Init(_ context.Context, collection string, indexes []driver.Index) error {
	b.initCalls++
	b.collection = collection
	b.indexes = append([]driver.Index(nil), indexes...)
	return b.initErr
}

func (b *testBackend) Save(_ context.Context, rec driver.Record) error {
	b.saveCalls++
	b.savedRecord = rec
	return b.saveErr
}

func (b *testBackend) Get(_ context.Context, _ string) (driver.Record, error) {
	if b.getErr != nil {
		return driver.Record{}, b.getErr
	}
	if b.getRecord.ID == "" {
		return driver.Record{}, driver.ErrNotFound
	}
	return b.getRecord, nil
}

func (b *testBackend) Delete(_ context.Context, id string) error {
	b.deleteCalls++
	b.deletedID = id
	return b.deleteErr
}

func (b *testBackend) Query(_ context.Context, q driver.Query) ([]driver.Record, error) {
	b.queryCalls++
	b.lastQuery = q
	return b.queryRecords, b.queryErr
}

func (b *testBackend) Count(_ context.Context, q driver.Query) (int64, error) {
	b.countCalls++
	b.lastQuery = q
	return b.countValue, b.countErr
}

func (b *testBackend) Values(_ context.Context, field string, q driver.Query, size int) ([]string, error) {
	b.valuesCalls++
	b.valuesField = field
	b.lastQuery = q
	b.valuesSize = size
	return b.valuesValue, b.valuesErr
}

func (b *testBackend) Close(context.Context) error { return nil }

func newTestMapper() *testMapper {
	return &testMapper{
		indexes: []driver.Index{
			{Field: "user_id"},
			{Field: "status"},
			{Field: "cost"},
		},
	}
}

func mustEncodeEntity(entity testEntity) []byte {
	data, _ := json.Marshal(entity)
	return data
}

// TestNewStore covers NewStore initialization: verifies successful init, nil backend, nil mapper, empty collection, and backend Init error.
func TestNewStore(t *testing.T) {
	backendInitErr := errors.New("backend init failed")

	cases := []struct {
		name       string
		backend    driver.Backend
		collection string
		mapper     driver.Mapper[testEntity]
		validate   func(*testing.T, *Store[testEntity], error, *testBackend)
	}{
		{
			name:       "init success",
			backend:    &testBackend{},
			collection: "jobs",
			mapper:     newTestMapper(),
			validate: func(t *testing.T, store *Store[testEntity], err error, backend *testBackend) {
				if err != nil {
					t.Errorf("NewStore() returned error: %v", err)
				}
				if store == nil {
					t.Errorf("NewStore() returned nil store")
				}
				if backend.initCalls != 1 {
					t.Errorf("backend Init() call count = %d, want 1", backend.initCalls)
				}
				if backend.collection != "jobs" {
					t.Errorf("backend collection = %q, want %q", backend.collection, "jobs")
				}
				if len(backend.indexes) != 3 {
					t.Errorf("backend indexes length = %d, want %d", len(backend.indexes), 3)
				}
			},
		},
		{
			name:       "nil backend",
			backend:    nil,
			collection: "jobs",
			mapper:     newTestMapper(),
			validate: func(t *testing.T, store *Store[testEntity], err error, _ *testBackend) {
				if store != nil {
					t.Errorf("NewStore() store = %#v, want nil", store)
				}
				if err == nil {
					t.Errorf("NewStore() error = nil, want error")
				}
			},
		},
		{
			name:       "nil mapper",
			backend:    &testBackend{},
			collection: "jobs",
			mapper:     nil,
			validate: func(t *testing.T, store *Store[testEntity], err error, backend *testBackend) {
				if store != nil {
					t.Errorf("NewStore() store = %#v, want nil", store)
				}
				if err == nil {
					t.Errorf("NewStore() error = nil, want error")
				}
				if backend.initCalls != 0 {
					t.Errorf("backend Init() call count = %d, want 0", backend.initCalls)
				}
			},
		},
		{
			name:       "empty collection",
			backend:    &testBackend{},
			collection: "",
			mapper: &testMapper{
				indexes: []driver.Index{{Field: "status"}},
			},
			validate: func(t *testing.T, store *Store[testEntity], err error, backend *testBackend) {
				if store != nil {
					t.Errorf("NewStore() store = %#v, want nil", store)
				}
				if err == nil {
					t.Errorf("NewStore() error = nil, want error")
				}
				if backend.initCalls != 0 {
					t.Errorf("backend Init() call count = %d, want 0", backend.initCalls)
				}
			},
		},
		{
			name: "backend init error",
			backend: &testBackend{
				initErr: backendInitErr,
			},
			collection: "jobs",
			mapper:     newTestMapper(),
			validate: func(t *testing.T, store *Store[testEntity], err error, backend *testBackend) {
				if store != nil {
					t.Errorf("NewStore() store = %#v, want nil", store)
				}
				if !errors.Is(err, backendInitErr) {
					t.Errorf("NewStore() error = %v, want %v", err, backendInitErr)
				}
				if backend.initCalls != 1 {
					t.Errorf("backend Init() call count = %d, want 1", backend.initCalls)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, err := NewStore[testEntity](t.Context(), tc.name, tc.backend, tc.collection, tc.mapper)

			typedBackend, _ := tc.backend.(*testBackend)
			tc.validate(t, store, err, typedBackend)
		})
	}
}

// TestStoreSave covers the Save path: verifies successful save, encode failure, empty ID, and backend write failure.
func TestStoreSave(t *testing.T) {
	saveErr := errors.New("save failed")

	cases := []struct {
		name     string
		mapper   *testMapper
		backend  *testBackend
		entity   testEntity
		validate func(*testing.T, error, *testBackend)
	}{
		{
			name:    "save success",
			mapper:  newTestMapper(),
			backend: &testBackend{},
			entity: testEntity{
				ID:     "job-20260409",
				UserID: "user-20260409",
				Status: "running",
				Cost:   8,
			},
			validate: func(t *testing.T, err error, backend *testBackend) {
				if err != nil {
					t.Errorf("Save() returned error: %v", err)
				}
				if backend.saveCalls != 1 {
					t.Errorf("backend Save() call count = %d, want 1", backend.saveCalls)
				}
				if backend.savedRecord.ID != "job-20260409" {
					t.Errorf("saved record id = %q, want %q", backend.savedRecord.ID, "job-20260409")
				}
				if backend.savedRecord.Fields["status"] != "running" {
					t.Errorf("saved record status = %v, want %q", backend.savedRecord.Fields["status"], "running")
				}
			},
		},
		{
			name: "encode failed",
			mapper: &testMapper{
				indexes:   newTestMapper().indexes,
				encodeErr: errors.New("marshal failed"),
			},
			backend: &testBackend{},
			entity: testEntity{
				ID:     "job-encode-error",
				UserID: "user-20260409",
				Status: "running",
				Cost:   5,
			},
			validate: func(t *testing.T, err error, backend *testBackend) {
				if !errors.Is(err, driver.ErrEncodeFailed) {
					t.Errorf("Save() error = %v, want ErrEncodeFailed", err)
				}
				if backend.saveCalls != 0 {
					t.Errorf("backend Save() call count = %d, want 0", backend.saveCalls)
				}
			},
		},
		{
			name: "empty id",
			mapper: &testMapper{
				indexes: newTestMapper().indexes,
				id:      "",
			},
			backend: &testBackend{},
			entity: testEntity{
				ID:     "",
				UserID: "user-20260409",
				Status: "running",
				Cost:   7,
			},
			validate: func(t *testing.T, err error, backend *testBackend) {
				if !errors.Is(err, driver.ErrInvalidField) {
					t.Errorf("Save() error = %v, want ErrInvalidField", err)
				}
				if backend.saveCalls != 0 {
					t.Errorf("backend Save() call count = %d, want 0", backend.saveCalls)
				}
			},
		},
		{
			name:    "backend save error",
			mapper:  newTestMapper(),
			backend: &testBackend{saveErr: saveErr},
			entity: testEntity{
				ID:     "job-save-error",
				UserID: "user-20260409",
				Status: "failed",
				Cost:   9,
			},
			validate: func(t *testing.T, err error, backend *testBackend) {
				if !errors.Is(err, saveErr) {
					t.Errorf("Save() error = %v, want %v", err, saveErr)
				}
				if backend.saveCalls != 1 {
					t.Errorf("backend Save() call count = %d, want 1", backend.saveCalls)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, err := NewStore[testEntity](t.Context(), tc.name, tc.backend, "jobs", tc.mapper)
			if err != nil {
				t.Errorf("NewStore() returned error: %v", err)
				return
			}

			err = store.Save(t.Context(), tc.entity)
			tc.validate(t, err, tc.backend)
		})
	}
}

// TestStoreGet covers the Get path: verifies decodable record found, record not found returns false, backend read failure, and decode failure.
func TestStoreGet(t *testing.T) {
	getErr := errors.New("get failed")

	cases := []struct {
		name     string
		backend  *testBackend
		validate func(*testing.T, testEntity, error)
	}{
		{
			name: "record found",
			backend: &testBackend{
				getRecord: driver.Record{
					ID:   "job-20260409",
					Data: mustEncodeEntity(testEntity{ID: "job-20260409", UserID: "user-20260409", Status: "running", Cost: 6}),
				},
			},
			validate: func(t *testing.T, entity testEntity, err error) {
				if err != nil {
					t.Errorf("Get() returned error: %v", err)
				}
				if entity.Status != "running" {
					t.Errorf("Get() entity status = %q, want %q", entity.Status, "running")
				}
			},
		},
		{
			name:    "record not found",
			backend: &testBackend{},
			validate: func(t *testing.T, entity testEntity, err error) {
				if !errors.Is(err, driver.ErrNotFound) {
					t.Errorf("Get() error = %v, want ErrNotFound", err)
				}
				if entity != (testEntity{}) {
					t.Errorf("Get() entity = %#v, want zero value", entity)
				}
			},
		},
		{
			name: "backend get error",
			backend: &testBackend{
				getErr: getErr,
			},
			validate: func(t *testing.T, entity testEntity, err error) {
				if !errors.Is(err, getErr) {
					t.Errorf("Get() error = %v, want %v", err, getErr)
				}
				if entity != (testEntity{}) {
					t.Errorf("Get() entity = %#v, want zero value", entity)
				}
			},
		},
		{
			name: "decode error",
			backend: &testBackend{
				getRecord: driver.Record{
					ID:   "job-decode-error",
					Data: []byte(`{"id":"job-decode-error"}`),
				},
			},
			validate: func(t *testing.T, entity testEntity, err error) {
				if err == nil {
					t.Errorf("Get() error = nil, want non-nil")
				}
				if entity != (testEntity{}) {
					t.Errorf("Get() entity = %#v, want zero value", entity)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mapper := newTestMapper()
			if tc.name == "decode error" {
				mapper.decodeErr = errors.New("decode failed")
			}

			store, err := NewStore[testEntity](t.Context(), tc.name, tc.backend, "jobs", mapper)
			if err != nil {
				t.Errorf("NewStore() returned error: %v", err)
				return
			}

			entity, getErr := store.Get(t.Context(), "job-20260409")
			tc.validate(t, entity, getErr)
		})
	}
}

// TestStoreDelete covers the Delete path: verifies the request is forwarded to the backend and backend errors are propagated.
func TestStoreDelete(t *testing.T) {
	deleteErr := errors.New("delete failed")

	cases := []struct {
		name     string
		backend  *testBackend
		validate func(*testing.T, error, *testBackend)
	}{
		{
			name:    "delete success",
			backend: &testBackend{},
			validate: func(t *testing.T, err error, backend *testBackend) {
				if err != nil {
					t.Errorf("Delete() returned error: %v", err)
				}
				if backend.deleteCalls != 1 {
					t.Errorf("backend Delete() call count = %d, want 1", backend.deleteCalls)
				}
				if backend.deletedID != "job-20260409" {
					t.Errorf("backend deleted id = %q, want %q", backend.deletedID, "job-20260409")
				}
			},
		},
		{
			name:    "delete error",
			backend: &testBackend{deleteErr: deleteErr},
			validate: func(t *testing.T, err error, backend *testBackend) {
				if !errors.Is(err, deleteErr) {
					t.Errorf("Delete() error = %v, want %v", err, deleteErr)
				}
				if backend.deleteCalls != 1 {
					t.Errorf("backend Delete() call count = %d, want 1", backend.deleteCalls)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, err := NewStore[testEntity](t.Context(), tc.name, tc.backend, "jobs", newTestMapper())
			if err != nil {
				t.Errorf("NewStore() returned error: %v", err)
				return
			}

			err = store.Delete(t.Context(), "job-20260409")
			tc.validate(t, err, tc.backend)
		})
	}
}

// TestStoreQuery covers the Query path: verifies valid queries return results; backend error and decode error are handled correctly.
func TestStoreQuery(t *testing.T) {
	queryErr := errors.New("query failed")

	cases := []struct {
		name     string
		backend  *testBackend
		query    driver.Query
		mapper   *testMapper
		validate func(*testing.T, []testEntity, error, *testBackend)
	}{
		{
			name: "query success",
			backend: &testBackend{
				queryRecords: []driver.Record{
					{ID: "job-running", Data: mustEncodeEntity(testEntity{ID: "job-running", UserID: "user-20260409", Status: "running", Cost: 6})},
				},
			},
			query: driver.Query{
				Filters: []driver.Filter{{Field: "status", Op: driver.OpEq, Value: "running"}},
				Sorts:   []driver.Sort{{Field: "cost", Desc: true}},
				Limit:   1,
				Offset:  2,
			},
			mapper: newTestMapper(),
			validate: func(t *testing.T, entities []testEntity, err error, backend *testBackend) {
				if err != nil {
					t.Errorf("Query() returned error: %v", err)
				}
				if len(entities) != 1 {
					t.Errorf("Query() result length = %d, want 1", len(entities))
				}
				if backend.queryCalls != 1 {
					t.Errorf("backend Query() call count = %d, want 1", backend.queryCalls)
				}
				if backend.lastQuery.Offset != 2 {
					t.Errorf("backend query offset = %d, want 2", backend.lastQuery.Offset)
				}
			},
		},
		{
			name:    "backend query error",
			backend: &testBackend{queryErr: queryErr},
			query: driver.Query{
				Filters: []driver.Filter{{Field: "status", Op: driver.OpEq, Value: "running"}},
			},
			mapper: newTestMapper(),
			validate: func(t *testing.T, entities []testEntity, err error, backend *testBackend) {
				if !errors.Is(err, queryErr) {
					t.Errorf("Query() error = %v, want %v", err, queryErr)
				}
				if len(entities) != 0 {
					t.Errorf("Query() result length = %d, want 0", len(entities))
				}
				if backend.queryCalls != 1 {
					t.Errorf("backend Query() call count = %d, want 1", backend.queryCalls)
				}
			},
		},
		{
			name: "decode error",
			backend: &testBackend{
				queryRecords: []driver.Record{
					{ID: "job-bad-record", Data: []byte(`{"id":"job-bad-record"}`)},
				},
			},
			query: driver.Query{
				Filters: []driver.Filter{{Field: "status", Op: driver.OpEq, Value: "running"}},
			},
			mapper: &testMapper{
				indexes:   newTestMapper().indexes,
				decodeErr: errors.New("decode failed"),
			},
			validate: func(t *testing.T, entities []testEntity, err error, backend *testBackend) {
				if !errors.Is(err, driver.ErrDecodeFailed) {
					t.Errorf("Query() error = %v, want ErrDecodeFailed", err)
				}
				if len(entities) != 0 {
					t.Errorf("Query() result length = %d, want 0", len(entities))
				}
				if backend.queryCalls != 1 {
					t.Errorf("backend Query() call count = %d, want 1", backend.queryCalls)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, err := NewStore[testEntity](t.Context(), tc.name, tc.backend, "jobs", tc.mapper)
			if err != nil {
				t.Errorf("NewStore() returned error: %v", err)
				return
			}

			entities, queryErr := store.Query(t.Context(), tc.query)
			tc.validate(t, entities, queryErr, tc.backend)
		})
	}
}

// TestStoreCount covers the Count path: verifies valid queries are forwarded to the backend, invalid pagination is rejected early, and backend errors are propagated.
func TestStoreCount(t *testing.T) {
	countErr := errors.New("count failed")

	cases := []struct {
		name     string
		backend  *testBackend
		query    driver.Query
		validate func(*testing.T, int64, error, *testBackend)
	}{
		{
			name: "count success",
			backend: &testBackend{
				countValue: 3,
			},
			query: driver.Query{
				Filters: []driver.Filter{{Field: "status", Op: driver.OpEq, Value: "running"}},
			},
			validate: func(t *testing.T, count int64, err error, backend *testBackend) {
				if err != nil {
					t.Errorf("Count() returned error: %v", err)
				}
				if count != 3 {
					t.Errorf("Count() = %d, want 3", count)
				}
				if backend.countCalls != 1 {
					t.Errorf("backend Count() call count = %d, want 1", backend.countCalls)
				}
			},
		},
		{
			name:    "invalid query",
			backend: &testBackend{},
			query: driver.Query{
				Limit: -1,
			},
			validate: func(t *testing.T, count int64, err error, backend *testBackend) {
				if !errors.Is(err, driver.ErrInvalidQuery) {
					t.Errorf("Count() error = %v, want ErrInvalidQuery", err)
				}
				if count != 0 {
					t.Errorf("Count() = %d, want 0", count)
				}
				if backend.countCalls != 0 {
					t.Errorf("backend Count() call count = %d, want 0", backend.countCalls)
				}
			},
		},
		{
			name:    "backend count error",
			backend: &testBackend{countErr: countErr},
			query: driver.Query{
				Filters: []driver.Filter{{Field: "status", Op: driver.OpEq, Value: "running"}},
			},
			validate: func(t *testing.T, count int64, err error, backend *testBackend) {
				if !errors.Is(err, countErr) {
					t.Errorf("Count() error = %v, want %v", err, countErr)
				}
				if count != 0 {
					t.Errorf("Count() = %d, want 0", count)
				}
				if backend.countCalls != 1 {
					t.Errorf("backend Count() call count = %d, want 1", backend.countCalls)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, err := NewStore[testEntity](t.Context(), tc.name, tc.backend, "jobs", newTestMapper())
			if err != nil {
				t.Errorf("NewStore() returned error: %v", err)
				return
			}

			count, countErr := store.Count(t.Context(), tc.query)
			tc.validate(t, count, countErr, tc.backend)
		})
	}
}

// TestStoreTerms covers the Terms aggregation path: verifies valid requests are forwarded to the backend; negative size is rejected, and backend errors are propagated.
func TestStoreTerms(t *testing.T) {
	valuesErr := errors.New("terms failed")

	cases := []struct {
		name     string
		backend  *testBackend
		field    string
		query    driver.Query
		size     int
		validate func(*testing.T, []string, error, *testBackend)
	}{
		{
			name: "terms success",
			backend: &testBackend{
				valuesValue: []string{"running", "completed"},
			},
			field: "status",
			query: driver.Query{
				Filters: []driver.Filter{{Field: "user_id", Op: driver.OpEq, Value: "user-20260409"}},
			},
			size: 5,
			validate: func(t *testing.T, terms []string, err error, backend *testBackend) {
				if err != nil {
					t.Errorf("Terms() returned error: %v", err)
				}
				if len(terms) != 2 {
					t.Errorf("Terms() result length = %d, want 2", len(terms))
				}
				if backend.valuesCalls != 1 {
					t.Errorf("backend Terms() call count = %d, want 1", backend.valuesCalls)
				}
				if backend.valuesField != "status" {
					t.Errorf("backend Terms() field = %q, want %q", backend.valuesField, "status")
				}
				if backend.valuesSize != 5 {
					t.Errorf("backend Terms() size = %d, want 5", backend.valuesSize)
				}
			},
		},
		{
			name:    "invalid size",
			backend: &testBackend{},
			field:   "status",
			size:    -1,
			validate: func(t *testing.T, terms []string, err error, backend *testBackend) {
				if !errors.Is(err, driver.ErrInvalidQuery) {
					t.Errorf("Terms() error = %v, want ErrInvalidQuery", err)
				}
				if len(terms) != 0 {
					t.Errorf("Terms() result length = %d, want 0", len(terms))
				}
				if backend.valuesCalls != 0 {
					t.Errorf("backend Terms() call count = %d, want 0", backend.valuesCalls)
				}
			},
		},
		{
			name:    "backend terms error",
			backend: &testBackend{valuesErr: valuesErr},
			field:   "status",
			query: driver.Query{
				Filters: []driver.Filter{{Field: "user_id", Op: driver.OpEq, Value: "user-20260409"}},
			},
			size: 5,
			validate: func(t *testing.T, terms []string, err error, backend *testBackend) {
				if !errors.Is(err, valuesErr) {
					t.Errorf("Terms() error = %v, want %v", err, valuesErr)
				}
				if len(terms) != 0 {
					t.Errorf("Terms() result length = %d, want 0", len(terms))
				}
				if backend.valuesCalls != 1 {
					t.Errorf("backend Terms() call count = %d, want 1", backend.valuesCalls)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, err := NewStore[testEntity](t.Context(), tc.name, tc.backend, "jobs", newTestMapper())
			if err != nil {
				t.Errorf("NewStore() returned error: %v", err)
				return
			}

			terms, valuesErr := store.Values(t.Context(), tc.field, tc.query, tc.size)
			tc.validate(t, terms, valuesErr, tc.backend)
		})
	}
}
