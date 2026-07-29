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
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"huatuo-bamai/internal/storage"
	"huatuo-bamai/internal/storage/driver"
)

func newStoreForTest(t *testing.T) Store {
	t.Helper()

	dsn := filepath.Join(t.TempDir(), "jobs.db")
	store, err := storage.NewFromConfig[*Job](t.Context(), &driver.Config{
		Driver:    "sqlite",
		SQLiteDSN: dsn,
	}, storageCollection(), storeMapper{})
	if err != nil {
		t.Errorf("New() returned error: %v", err)
		return nil
	}

	return &storageStore{store: store}
}

func sampleStoredJobs(baseTime time.Time) []*Job {
	return []*Job{
		{
			Type:         "profiling_cpu",
			ID:           "job-store-alpha",
			Username:     "operator-2026",
			UserID:       "operator-2026",
			ContainerID:  "payment-worker",
			Hostname:     "huatuo-dev",
			AgentTaskID:  "agent-task-alpha",
			Status:       JobStatusCompleted,
			Duration:     120,
			TraceTimeout: 120,
			CreatedAt:    baseTime,
			FinishedAt:   baseTime.Add(2 * time.Minute),
			AgentTask: AgentTaskRequest{
				TracerName:   "profiler",
				TraceTimeout: 120,
				DataType:     "db-json",
			},
			Result: Result{
				URL: "s3://huatuo-region/job-store-alpha",
			},
			UpdatedAt:   baseTime.Add(2 * time.Minute),
			PrivateData: json.RawMessage(`{"memory_mode":"object_alloc"}`),
		},
		{
			Type:         "tracing",
			ID:           "job-store-beta",
			Username:     "reviewer-2026",
			UserID:       "reviewer-2026",
			ContainerID:  "db-worker",
			Hostname:     "huatuo-dev",
			AgentTaskID:  "agent-task-beta",
			Status:       JobStatusStopped,
			Duration:     60,
			TraceTimeout: 60,
			CreatedAt:    baseTime.Add(1 * time.Hour),
			FinishedAt:   baseTime.Add(61 * time.Minute),
			AgentTask: AgentTaskRequest{
				TracerName:   "tracer",
				TraceTimeout: 60,
				DataType:     "db",
			},
			UpdatedAt: baseTime.Add(61 * time.Minute),
		},
	}
}

// TestStorageStoreSQLiteIntegration covers the full job store round-trip through the SQLite backend: verifies save, get by ID, list with filters, delete, and PrivateData fields all persist and load correctly.
func TestStorageStoreSQLiteIntegration(t *testing.T) {
	store := newStoreForTest(t)
	if store == nil {
		return
	}

	baseTime := time.Date(2026, 4, 9, 13, 0, 0, 0, time.UTC)
	jobs := sampleStoredJobs(baseTime)
	for _, storedJob := range jobs {
		if err := store.Save(t.Context(), storedJob); err != nil {
			t.Errorf("Save(%q) returned error: %v", storedJob.ID, err)
		}
	}

	gotJob, err := store.Get(t.Context(), "job-store-alpha")
	if err != nil {
		t.Errorf("Get() returned error: %v", err)
	}
	if gotJob == nil {
		t.Errorf("Get() returned nil job")
		return
	}
	if gotJob.Result.URL != "s3://huatuo-region/job-store-alpha" {
		t.Errorf("Get() result url = %q, want %q", gotJob.Result.URL, "s3://huatuo-region/job-store-alpha")
	}
	var privateData map[string]string
	if err := json.Unmarshal(gotJob.PrivateData, &privateData); err != nil {
		t.Fatalf("unmarshal private data: %v", err)
	}
	if privateData["memory_mode"] != "object_alloc" {
		t.Errorf("Get() memory_mode = %v, want %q", privateData["memory_mode"], "object_alloc")
	}

	listedJobs, err := store.List(t.Context(), &JobQuery{
		UserID:   "operator-2026",
		IsAdmin:  false,
		Hostname: "huatuo-dev",
		Types:    []JobType{JobTypeProfilingCPU},
	})
	if err != nil {
		t.Errorf("List() returned error: %v", err)
	}
	if len(listedJobs) != 1 {
		t.Errorf("List() result length = %d, want 1", len(listedJobs))
	}
	if len(listedJobs) == 1 && listedJobs[0].ID != "job-store-alpha" {
		t.Errorf("List() first id = %q, want %q", listedJobs[0].ID, "job-store-alpha")
	}

	if err := store.Delete(t.Context(), "job-store-beta"); err != nil {
		t.Errorf("Delete() returned error: %v", err)
	}

	_, err = store.Get(t.Context(), "job-store-beta")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get() after delete error = %v, want %v", err, ErrNotFound)
	}
}

func TestValidateJobQueryRejectsUnsafeSort(t *testing.T) {
	err := validateJobQuery(&JobQuery{Sort: "created_at; DROP TABLE jobs"})
	if !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("validateJobQuery() error=%v, want ErrInvalidQuery", err)
	}
}

func TestStorageMapperUsesJobFieldNames(t *testing.T) {
	createdAt := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	entity := &Job{
		ID:           "job-2026",
		ContainerID:  "container-2026",
		Hostname:     "host-2026",
		ErrorMessage: "agent failed",
		CreatedAt:    createdAt,
		FinishedAt:   createdAt.Add(time.Minute),
	}

	data, err := (storeMapper{}).Encode(entity)
	if err != nil {
		t.Fatalf("Encode() error=%v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("json.Unmarshal() error=%v", err)
	}

	for _, field := range []string{
		"id",
		"username",
		"container_id",
		"hostname",
		"agent_task_id",
		"error_message",
		"trace_timeout",
		"created_at",
		"finished_at",
		"agent_task",
		"result",
		"updated_at",
	} {
		if _, ok := payload[field]; !ok {
			t.Errorf("encoded field %q is missing", field)
		}
	}
	for _, field := range []string{
		"job_id",
		"user_name",
		"agent_job_id",
		"error",
		"timeout",
		"start_time",
		"end_time",
		"args",
		"results",
		"last_update",
	} {
		if _, ok := payload[field]; ok {
			t.Errorf("legacy encoded field %q is present", field)
		}
	}

	fields := storageFields(entity)
	for _, field := range []string{"container_id", "hostname", "created_at", "finished_at"} {
		if _, ok := fields[field]; !ok {
			t.Errorf("storage field %q is missing", field)
		}
	}
}

func TestToStorageQueryUsesJobFieldNames(t *testing.T) {
	query := toStorageQuery(&JobQuery{
		ContainerID: "container-2026",
		Hostname:    "host-2026",
	})

	if len(query.Filters) != 2 {
		t.Fatalf("filter count=%d, want 2", len(query.Filters))
	}
	if query.Filters[0].Field != "container_id" {
		t.Errorf("first filter=%q, want container_id", query.Filters[0].Field)
	}
	if query.Filters[1].Field != "hostname" {
		t.Errorf("second filter=%q, want hostname", query.Filters[1].Field)
	}
	if len(query.Sorts) == 0 || query.Sorts[0].Field != "created_at" || !query.Sorts[0].Desc {
		t.Errorf("default sorts=%v, want descending created_at", query.Sorts)
	}
}
