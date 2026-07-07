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
	}, StorageCollection(), storeMapper{})
	if err != nil {
		t.Errorf("New() returned error: %v", err)
		return nil
	}

	return &storageStore{store: store}
}

func sampleStoredJobs(baseTime time.Time) []*Job {
	return []*Job{
		{
			Type:        "profiling_cpu",
			JobID:       "job-store-alpha",
			UserName:    "operator-2026",
			UserID:      "operator-2026",
			Container:   "payment-worker",
			Host:        "huatuo-dev",
			AgentTaskID: "agent-task-alpha",
			Status:      JobStatusCompleted,
			Duration:    120,
			Timeout:     120,
			StartTime:   baseTime,
			EndTime:     baseTime.Add(2 * time.Minute),
			Args: NewAgentTaskReq{
				TracerName:   "profiler",
				TraceTimeout: 120,
				DataType:     "db-json",
			},
			Results: Result{
				URL: "s3://huatuo-region/job-store-alpha",
			},
			LastUpdate: baseTime.Add(2 * time.Minute),
			PrivateData: map[string]any{
				"memory_mode": "object_alloc",
			},
		},
		{
			Type:        "tracing",
			JobID:       "job-store-beta",
			UserName:    "reviewer-2026",
			UserID:      "reviewer-2026",
			Container:   "db-worker",
			Host:        "huatuo-dev",
			AgentTaskID: "agent-task-beta",
			Status:      JobStatusStopped,
			Duration:    60,
			Timeout:     60,
			StartTime:   baseTime.Add(1 * time.Hour),
			EndTime:     baseTime.Add(61 * time.Minute),
			Args: NewAgentTaskReq{
				TracerName:   "tracer",
				TraceTimeout: 60,
				DataType:     "db",
			},
			LastUpdate: baseTime.Add(61 * time.Minute),
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
		if err := store.Save(storedJob); err != nil {
			t.Errorf("Save(%q) returned error: %v", storedJob.JobID, err)
		}
	}

	gotJob, err := store.Get("job-store-alpha")
	if err != nil {
		t.Errorf("Get() returned error: %v", err)
	}
	if gotJob == nil {
		t.Errorf("Get() returned nil job")
		return
	}
	if gotJob.Results.URL != "s3://huatuo-region/job-store-alpha" {
		t.Errorf("Get() results url = %q, want %q", gotJob.Results.URL, "s3://huatuo-region/job-store-alpha")
	}
	if gotJob.PrivateData["memory_mode"] != "object_alloc" {
		t.Errorf("Get() memory_mode = %v, want %q", gotJob.PrivateData["memory_mode"], "object_alloc")
	}

	listedJobs, err := store.List(&JobQuery{
		UserID:  "operator-2026",
		IsAdmin: false,
		Host:    "huatuo-dev",
		Type:    "profiling_cpu",
	})
	if err != nil {
		t.Errorf("List() returned error: %v", err)
	}
	if len(listedJobs) != 1 {
		t.Errorf("List() result length = %d, want 1", len(listedJobs))
	}
	if len(listedJobs) == 1 && listedJobs[0].JobID != "job-store-alpha" {
		t.Errorf("List() first id = %q, want %q", listedJobs[0].JobID, "job-store-alpha")
	}

	if err := store.Delete("job-store-beta"); err != nil {
		t.Errorf("Delete() returned error: %v", err)
	}

	_, err = store.Get("job-store-beta")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get() after delete error = %v, want %v", err, ErrNotFound)
	}
}
