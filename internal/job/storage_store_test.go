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

package job

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/storage/driver"
	"github.com/ccfos/huatuo/pkg/observation"
	"github.com/ccfos/huatuo/pkg/profiling"
)

func openTestStore(t *testing.T) Store {
	t.Helper()
	store, err := newStore(t.Context(), filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("newStore() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return store
}

func storedTestJob(id, userID, host string, status Status, createdAt time.Time) *Job {
	job := &Job{
		ID:       id,
		Kind:     KindProfiling,
		UserID:   userID,
		Hostname: host,
		Duration: time.Minute,
		Scope:    observation.ScopeHost,
		Spec: Spec{Profiling: &profiling.Spec{
			Type:     profiling.TypeCPU,
			Language: profiling.LanguageGo,
			Mode:     profiling.ModeOnCPU,
		}},
		Status:    status,
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
		revision:  1,
	}
	if isTerminal(status) {
		job.EndedAt = createdAt.Add(time.Minute)
	}
	if status == StatusTerminal {
		job.Terminal = &TerminalResult{Outcome: OutcomeCompleted}
	}
	return job
}

func TestStorageStoreRoundTripQueryAndCompareAndSwap(t *testing.T) {
	store := openTestStore(t)
	base := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	first := storedTestJob("job-1", "user-1", "node-1", StatusPending, base)
	second := storedTestJob("job-2", "user-2", "node-1", StatusTerminal, base.Add(time.Minute))
	for _, job := range []*Job{first, second} {
		if err := store.Create(t.Context(), job); err != nil {
			t.Fatalf("Create(%q) error = %v", job.ID, err)
		}
	}
	if err := store.Create(t.Context(), first); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate Create() error = %v, want ErrAlreadyExists", err)
	}

	updated := cloneJob(first)
	updated.Status = StatusRunning
	updated.StartedAt = base.Add(time.Second)
	updated.ExecutionDeadline = base.Add(2 * time.Minute)
	updated.PendingDeadline = base.Add(time.Minute)
	updated.UpdatedAt = base.Add(time.Second)
	stale := cloneJob(updated)
	if _, err := store.Save(t.Context(), updated); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if _, err := store.Save(t.Context(), stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("Save() stale revision error = %v, want ErrConflict", err)
	}

	got, err := store.Get(t.Context(), first.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Status != StatusRunning || !got.StartedAt.Equal(updated.StartedAt) || got.revision != 2 {
		t.Fatalf("Get() = (%q, %s, revision %d)", got.Status, got.StartedAt, got.revision)
	}
	listed, err := store.List(t.Context(), &Query{
		UserID:   "user-1",
		Hostname: "node-1",
		Statuses: []Status{StatusRunning},
		Kinds:    []Kind{KindProfiling},
		Subtypes: []string{string(profiling.TypeCPU)},
		Limit:    10,
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(listed) != 1 || listed[0].ID != first.ID || listed[0].revision != 2 {
		t.Fatalf("List() IDs = %v, want [%s]", jobIDs(listed), first.ID)
	}
}

func TestStorageStoreRevisionConflictsForSameStatusUpdates(t *testing.T) {
	store := openTestStore(t)
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	job := storedTestJob("job-1", "user-1", "node-1", StatusPending, now)
	if err := store.Create(t.Context(), job); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	first := cloneJob(job)
	first.PendingDeadline = now.Add(time.Minute)
	first.UpdatedAt = now.Add(time.Second)
	second := cloneJob(first)
	second.NodeUnavailableDeadline = now.Add(2 * time.Minute)
	if _, err := store.Save(t.Context(), first); err != nil {
		t.Fatalf("first Save() error = %v", err)
	}
	if _, err := store.Save(t.Context(), second); !errors.Is(err, ErrConflict) {
		t.Fatalf("second Save() error = %v, want ErrConflict", err)
	}
}

func TestStorageStoreRejectsInvalidRevision(t *testing.T) {
	store := openTestStore(t)
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	job := storedTestJob("job-1", "user-1", "node-1", StatusPending, now)
	if err := store.Create(t.Context(), job); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	job.revision = 0
	if _, err := store.Save(t.Context(), job); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("Save() error = %v, want ErrInvalidQuery", err)
	}
}

func TestStorageStoreDeletesOnlyExpiredTerminalJobs(t *testing.T) {
	store := openTestStore(t)
	base := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	oldTerminal := storedTestJob("old", "user-1", "node-1", StatusTerminal, base)
	recentTerminal := storedTestJob("recent", "user-1", "node-1", StatusTerminal, base.Add(2*time.Hour))
	active := storedTestJob("active", "user-1", "node-1", StatusPending, base)
	for _, job := range []*Job{oldTerminal, recentTerminal, active} {
		if err := store.Create(t.Context(), job); err != nil {
			t.Fatalf("Create(%q) error = %v", job.ID, err)
		}
	}

	deleted, err := store.DeleteTerminalBefore(t.Context(), base.Add(90*time.Minute), 10)
	if err != nil || deleted != 1 {
		t.Fatalf("DeleteTerminalBefore() = (%d, %v), want (1, nil)", deleted, err)
	}
	if _, err := store.Get(t.Context(), oldTerminal.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(old) error = %v, want ErrNotFound", err)
	}
	for _, id := range []string{recentTerminal.ID, active.ID} {
		if _, err := store.Get(t.Context(), id); err != nil {
			t.Fatalf("Get(%q) error = %v", id, err)
		}
	}
}

func TestStorageStoreRejectsCurrentRecordWithoutRevision(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "jobs.db")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE jobs (
		id TEXT PRIMARY KEY,
		data BLOB NOT NULL,
		fields TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create jobs table: %v", err)
	}
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	job := storedTestJob("job-1", "user-1", "node-1", StatusPending, now)
	data, err := (recordMapper{}).Encode(job)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO jobs (id, data, fields) VALUES (?, ?, ?)`,
		job.ID,
		data,
		`{"status":"pending"}`,
	); err != nil {
		t.Fatalf("insert Job: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	store, err := newStore(t.Context(), dsn)
	if err != nil {
		t.Fatalf("newStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	_, err = store.Get(t.Context(), job.ID)
	if err == nil || !strings.Contains(err.Error(), "field revision: value is required") {
		t.Fatalf("Get() error = %v, want missing revision error", err)
	}
}

func TestValidateQuerySortRejectsUnsafeSort(t *testing.T) {
	err := validateQuerySort(&Query{Sort: "created_at; DROP TABLE jobs"})
	if !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("validateQuerySort() error = %v, want ErrInvalidQuery", err)
	}
}

func jobIDs(jobs []*Job) []string {
	ids := make([]string, len(jobs))
	for i, job := range jobs {
		ids[i] = job.ID
	}
	return ids
}

func TestValidateQuerySortRejectsLoneDescendingField(t *testing.T) {
	err := validateQuerySort(&Query{Sort: "-"})
	if !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("validateQuerySort() error = %v, want ErrInvalidQuery", err)
	}
}

func TestBuildStorageQueryFiltersByID(t *testing.T) {
	query, err := buildStorageQuery(&Query{ID: "job-store-alpha"})
	if err != nil {
		t.Fatalf("buildStorageQuery() error = %v", err)
	}
	var found bool
	for _, filter := range query.Filters {
		if filter.Field != "id" {
			continue
		}
		found = true
		if filter.Op != driver.OpEq {
			t.Fatalf("id filter op = %v, want %v", filter.Op, driver.OpEq)
		}
		if filter.Value != "job-store-alpha" {
			t.Fatalf("id filter value = %v, want %q", filter.Value, "job-store-alpha")
		}
	}
	if !found {
		t.Fatalf("buildStorageQuery() filters = %v, want an id filter", query.Filters)
	}
}
