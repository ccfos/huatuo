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
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"huatuo-bamai/pkg/observation"
	"huatuo-bamai/pkg/profiling"
)

func openTestStore(t *testing.T) Store {
	t.Helper()
	store, err := newStore(t.Context(), filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("newStore() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(t.Context()); err != nil {
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
	updated.StartAttemptedAt = base
	updated.PendingDeadline = base.Add(time.Minute)
	updated.UpdatedAt = base.Add(time.Second)
	if err := store.Save(t.Context(), updated, StatusTerminal); !errors.Is(err, ErrConflict) {
		t.Fatalf("Save() stale status error = %v, want ErrConflict", err)
	}
	if err := store.Save(t.Context(), updated, StatusPending); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := store.Get(t.Context(), first.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Status != StatusRunning || !got.StartedAt.Equal(updated.StartedAt) {
		t.Fatalf("Get() = (%q, %s)", got.Status, got.StartedAt)
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
	if len(listed) != 1 || listed[0].ID != first.ID {
		t.Fatalf("List() IDs = %v, want [%s]", jobIDs(listed), first.ID)
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

func TestStorageMigrationConvertsLegacyActiveJobToOperationLost(t *testing.T) {
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
		t.Fatalf("create legacy jobs table: %v", err)
	}
	base := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	legacy := legacyStoragePayload{
		Type:      "profiling_cpu",
		ID:        "legacy-job",
		Username:  "legacy-user",
		Hostname:  "node-1",
		Status:    "running",
		Duration:  120,
		CreatedAt: base,
		UpdatedAt: base,
		AgentTask: legacyAgentTaskRequest{TracerArgs: []string{
			"--duration", "60", "--language", "go",
		}},
	}
	payload, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO jobs (id, data, fields) VALUES (?, ?, ?)`,
		legacy.ID,
		payload,
		`{"status":"running"}`,
	); err != nil {
		t.Fatalf("insert legacy Job: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	store, err := newStore(t.Context(), dsn)
	if err != nil {
		t.Fatalf("newStore() migration error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close(t.Context()) })
	got, err := store.Get(t.Context(), legacy.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Status != StatusTerminal || got.Terminal == nil || got.Terminal.Outcome != OutcomeUnknown ||
		got.Terminal.Reason != FailureReasonOperationLost {
		t.Fatalf("migrated Job = (%q, %+v)", got.Status, got.Terminal)
	}
	if got.Kind != KindProfiling || got.Duration != time.Minute {
		t.Fatalf("migrated Job kind/duration = (%q, %s)", got.Kind, got.Duration)
	}
}

func TestValidateQueryRejectsUnsafeSort(t *testing.T) {
	err := validateQuery(&Query{Sort: "created_at; DROP TABLE jobs"})
	if !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("validateQuery() error = %v, want ErrInvalidQuery", err)
	}
}

func jobIDs(jobs []*Job) []string {
	ids := make([]string, len(jobs))
	for i, job := range jobs {
		ids[i] = job.ID
	}
	return ids
}
