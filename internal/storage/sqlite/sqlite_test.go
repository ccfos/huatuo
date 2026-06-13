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
	"errors"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"huatuo-bamai/internal/storage/driver"
	storagesqlite "huatuo-bamai/internal/storage/sqlite"
)

type backendTestEntity struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Status    string    `json:"status"`
	Priority  int64     `json:"priority"`
	CreatedAt time.Time `json:"created_at"`
}

func sqliteIndexes() []driver.Index {
	return []driver.Index{
		{Field: "user_id"},
		{Field: "status"},
		{Field: "priority"},
		{Field: "created_at"},
	}
}

func newSQLiteBackendForTest(t *testing.T) *storagesqlite.Storage {
	t.Helper()

	dsn := filepath.Join(t.TempDir(), "storage.db")
	backend, err := storagesqlite.NewBackend(dsn)
	if err != nil {
		t.Errorf("NewBackend() returned error: %v", err)
		return nil
	}

	t.Cleanup(func() {
		if closeErr := backend.Close(t.Context()); closeErr != nil {
			t.Errorf("backend Close() returned error: %v", closeErr)
		}
	})

	return backend
}

func seedSQLiteRecords(t *testing.T, backend *storagesqlite.Storage, records []driver.Record) {
	t.Helper()

	for _, rec := range records {
		if err := backend.Save(t.Context(), rec); err != nil {
			t.Errorf("backend Save(%q) returned error: %v", rec.ID, err)
		}
	}
}

func mustMarshalEntity(entity *backendTestEntity) []byte {
	data, _ := json.Marshal(entity)
	return data
}

// TestSQLiteBackendCRUD covers basic SQLite backend CRUD: verifies table/index creation, record save, lookup by primary key, delete of existing record, and delete of missing record without error.
func TestSQLiteBackendCRUD(t *testing.T) {
	backend := newSQLiteBackendForTest(t)
	if backend == nil {
		return
	}

	initErr := backend.Init(t.Context(), "jobs", []driver.Index{
		{Field: "user_id"},
		{Field: "status"},
	})
	if initErr != nil {
		t.Errorf("backend Init() returned error: %v", initErr)
		return
	}

	createdAt := time.Date(2026, 4, 9, 10, 0, 0, 0, time.UTC)
	record := driver.Record{
		ID:   "job-20260409",
		Data: mustMarshalEntity(&backendTestEntity{ID: "job-20260409", UserID: "user-20260409", Status: "running", Priority: 8, CreatedAt: createdAt}),
		Fields: map[string]any{
			"user_id":    "user-20260409",
			"status":     "running",
			"priority":   int64(8),
			"created_at": createdAt,
		},
	}

	if err := backend.Save(t.Context(), record); err != nil {
		t.Errorf("backend Save() returned error: %v", err)
		return
	}

	gotRecord, err := backend.Get(t.Context(), "job-20260409")
	if err != nil {
		t.Errorf("backend Get() returned error: %v", err)
	}
	if gotRecord.ID != "job-20260409" {
		t.Errorf("backend Get() id = %q, want %q", gotRecord.ID, "job-20260409")
	}
	if gotRecord.Fields["status"] != "running" {
		t.Errorf("backend Get() status = %v, want %q", gotRecord.Fields["status"], "running")
	}

	if err := backend.Delete(t.Context(), "job-20260409"); err != nil {
		t.Errorf("backend Delete() returned error: %v", err)
	}

	_, err = backend.Get(t.Context(), "job-20260409")
	if !errors.Is(err, driver.ErrNotFound) {
		t.Errorf("backend Get() after delete = %v, want ErrNotFound", err)
	}

	if err := backend.Delete(t.Context(), "job-not-exist"); err != nil {
		t.Errorf("backend Delete() for missing record returned error: %v", err)
	}
}

// TestSQLiteBackendQuery covers SQLite backend querying: verifies equality filter, range filter, IN filter, sorting, pagination, count, and rejection of invalid pagination.
func TestSQLiteBackendQuery(t *testing.T) {
	backend := newSQLiteBackendForTest(t)
	if backend == nil {
		return
	}

	if err := backend.Init(t.Context(), "jobs", sqliteIndexes()); err != nil {
		t.Errorf("backend Init() returned error: %v", err)
		return
	}

	baseTime := time.Date(2026, 4, 9, 8, 0, 0, 0, time.UTC)
	seedSQLiteRecords(t, backend, []driver.Record{
		{
			ID:   "job-running-top",
			Data: mustMarshalEntity(&backendTestEntity{ID: "job-running-top", UserID: "user-alpha", Status: "running", Priority: 9, CreatedAt: baseTime.Add(1 * time.Hour)}),
			Fields: map[string]any{
				"user_id":    "user-alpha",
				"status":     "running",
				"priority":   int64(9),
				"created_at": baseTime.Add(1 * time.Hour),
			},
		},
		{
			ID:   "job-running-middle",
			Data: mustMarshalEntity(&backendTestEntity{ID: "job-running-middle", UserID: "user-beta", Status: "running", Priority: 6, CreatedAt: baseTime.Add(2 * time.Hour)}),
			Fields: map[string]any{
				"user_id":    "user-beta",
				"status":     "running",
				"priority":   int64(6),
				"created_at": baseTime.Add(2 * time.Hour),
			},
		},
		{
			ID:   "job-running-low",
			Data: mustMarshalEntity(&backendTestEntity{ID: "job-running-low", UserID: "user-alpha", Status: "running", Priority: 3, CreatedAt: baseTime.Add(3 * time.Hour)}),
			Fields: map[string]any{
				"user_id":    "user-alpha",
				"status":     "running",
				"priority":   int64(3),
				"created_at": baseTime.Add(3 * time.Hour),
			},
		},
		{
			ID:   "job-finished",
			Data: mustMarshalEntity(&backendTestEntity{ID: "job-finished", UserID: "user-gamma", Status: "completed", Priority: 7, CreatedAt: baseTime.Add(4 * time.Hour)}),
			Fields: map[string]any{
				"user_id":    "user-gamma",
				"status":     "completed",
				"priority":   int64(7),
				"created_at": baseTime.Add(4 * time.Hour),
			},
		},
	})

	records, err := backend.Query(t.Context(), driver.Query{
		Filters: []driver.Filter{
			{Field: "status", Op: driver.OpEq, Value: "running"},
			{Field: "priority", Op: driver.OpGte, Value: int64(3)},
			{Field: "user_id", Op: driver.OpIn, Value: []string{"user-alpha", "user-beta"}},
		},
		Sorts: []driver.Sort{
			{Field: "priority", Desc: true},
		},
		Limit:  1,
		Offset: 1,
	})
	if err != nil {
		t.Errorf("backend Query() returned error: %v", err)
		return
	}
	if len(records) != 1 {
		t.Errorf("backend Query() result length = %d, want 1", len(records))
	} else if records[0].ID != "job-running-middle" {
		t.Errorf("backend Query() first id = %q, want %q", records[0].ID, "job-running-middle")
	}

	count, err := backend.Count(t.Context(), driver.Query{
		Filters: []driver.Filter{
			{Field: "status", Op: driver.OpEq, Value: "running"},
		},
	})
	if err != nil {
		t.Errorf("backend Count() returned error: %v", err)
	}
	if count != 3 {
		t.Errorf("backend Count() = %d, want 3", count)
	}

	_, err = backend.Query(t.Context(), driver.Query{Limit: -1})
	if err == nil {
		t.Errorf("backend Query() error = nil for negative limit, want error")
	}
}

// TestSQLiteBackendTerms covers SQLite backend Terms aggregation: verifies it returns distinct field values matching the filter, skips missing fields, and respects the size limit.
func TestSQLiteBackendTerms(t *testing.T) {
	backend := newSQLiteBackendForTest(t)
	if backend == nil {
		return
	}

	if err := backend.Init(t.Context(), "jobs", sqliteIndexes()); err != nil {
		t.Errorf("backend Init() returned error: %v", err)
		return
	}

	baseTime := time.Date(2026, 4, 9, 8, 0, 0, 0, time.UTC)
	seedSQLiteRecords(t, backend, []driver.Record{
		{
			ID:   "job-running-alpha",
			Data: mustMarshalEntity(&backendTestEntity{ID: "job-running-alpha", UserID: "user-alpha", Status: "running", Priority: 9, CreatedAt: baseTime}),
			Fields: map[string]any{
				"user_id":    "user-alpha",
				"status":     "running",
				"priority":   int64(9),
				"created_at": baseTime,
			},
		},
		{
			ID:   "job-running-beta",
			Data: mustMarshalEntity(&backendTestEntity{ID: "job-running-beta", UserID: "user-beta", Status: "running", Priority: 6, CreatedAt: baseTime.Add(time.Hour)}),
			Fields: map[string]any{
				"user_id":    "user-beta",
				"status":     "running",
				"priority":   int64(6),
				"created_at": baseTime.Add(time.Hour),
			},
		},
		{
			ID:   "job-running-alpha-later",
			Data: mustMarshalEntity(&backendTestEntity{ID: "job-running-alpha-later", UserID: "user-alpha", Status: "running", Priority: 3, CreatedAt: baseTime.Add(2 * time.Hour)}),
			Fields: map[string]any{
				"user_id":    "user-alpha",
				"status":     "running",
				"priority":   int64(3),
				"created_at": baseTime.Add(2 * time.Hour),
			},
		},
		{
			ID:   "job-completed-gamma",
			Data: mustMarshalEntity(&backendTestEntity{ID: "job-completed-gamma", UserID: "user-gamma", Status: "completed", Priority: 7, CreatedAt: baseTime.Add(3 * time.Hour)}),
			Fields: map[string]any{
				"user_id":    "user-gamma",
				"status":     "completed",
				"priority":   int64(7),
				"created_at": baseTime.Add(3 * time.Hour),
			},
		},
		{
			ID:   "job-running-without-user",
			Data: mustMarshalEntity(&backendTestEntity{ID: "job-running-without-user", Status: "running", Priority: 1, CreatedAt: baseTime.Add(4 * time.Hour)}),
			Fields: map[string]any{
				"status":     "running",
				"priority":   int64(1),
				"created_at": baseTime.Add(4 * time.Hour),
			},
		},
	})

	terms, err := backend.Values(t.Context(), "user_id", driver.Query{
		Filters: []driver.Filter{{Field: "status", Op: driver.OpEq, Value: "running"}},
	}, 10)
	if err != nil {
		t.Errorf("backend Terms() returned error: %v", err)
		return
	}

	sort.Strings(terms)
	expectedTerms := []string{"user-alpha", "user-beta"}
	if len(terms) != len(expectedTerms) {
		t.Errorf("backend Terms() count = %d, want %d", len(terms), len(expectedTerms))
	}
	for index, expectedTerm := range expectedTerms {
		if index >= len(terms) {
			break
		}
		if terms[index] != expectedTerm {
			t.Errorf("backend Terms()[%d] = %q, want %q", index, terms[index], expectedTerm)
		}
	}

	limitedTerms, err := backend.Values(t.Context(), "user_id", driver.Query{
		Filters: []driver.Filter{{Field: "status", Op: driver.OpEq, Value: "running"}},
	}, 1)
	if err != nil {
		t.Errorf("backend Terms() with limit returned error: %v", err)
	}
	if len(limitedTerms) != 1 {
		t.Errorf("backend Terms() limited count = %d, want 1", len(limitedTerms))
	}
}
