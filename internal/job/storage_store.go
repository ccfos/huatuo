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
	"context"
	"encoding/json"
	"fmt"
	"time"

	"huatuo-bamai/internal/storage"
	"huatuo-bamai/internal/storage/driver"
)

type storageStore struct {
	store *storage.Store[*Job]
}

type storagePayload struct {
	Type        string          `json:"type"`
	JobID       string          `json:"job_id"`
	UserName    string          `json:"user_name"`
	UserID      string          `json:"user_id"`
	Container   string          `json:"container"`
	Host        string          `json:"host"`
	AgentTaskID string          `json:"agent_job_id"`
	Status      JobStatus       `json:"status"`
	Error       string          `json:"error,omitempty"`
	Duration    int             `json:"duration"`
	Timeout     int             `json:"timeout"`
	StartTime   time.Time       `json:"start_time"`
	EndTime     time.Time       `json:"end_time"`
	Args        NewAgentTaskReq `json:"args"`
	Results     Result          `json:"results,omitempty"`
	LastUpdate  time.Time       `json:"last_update"`
	PrivateData map[string]any  `json:"private_data,omitempty"`
}

type storeMapper struct{}

func StorageCollection() string {
	return "jobs"
}

func StorageFields(entity *Job) map[string]any {
	return map[string]any{
		"user_id":    entity.UserID,
		"container":  entity.Container,
		"host":       entity.Host,
		"status":     string(entity.Status),
		"type":       entity.Type,
		"start_time": entity.StartTime,
	}
}

func StorageIndexes() []string {
	return []string{
		"user_id",
		"container",
		"host",
		"status",
		"type",
		"start_time",
	}
}

// defaultJobsDBPath is the SQLite file used when no DSN is provided via ManagerConfig.
const defaultJobsDBPath = "jobs.db"

func newStore(ctx context.Context, dsn string) (Store, error) {
	if dsn == "" {
		dsn = defaultJobsDBPath
	}
	store, err := storage.NewFromConfig(
		ctx,
		&driver.Config{
			Driver:    "sqlite",
			SQLiteDSN: dsn,
		},
		storeMapper{},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize job backend: %w", err)
	}

	return &storageStore{store: store}, nil
}

func (s *storageStore) Get(jobID string) (*Job, error) {
	entity, err := s.store.Get(context.Background(), jobID)
	if err != nil {
		return nil, err
	}

	return cloneJob(entity), nil
}

func (s *storageStore) Save(jobEntity *Job) error {
	if jobEntity == nil {
		return fmt.Errorf("job store: job is nil")
	}

	return s.store.Save(context.Background(), jobEntity)
}

func (s *storageStore) Delete(jobID string) error {
	return s.store.Delete(context.Background(), jobID)
}

func (s *storageStore) List(query *JobQuery) ([]*Job, error) {
	result, err := s.store.Query(context.Background(), toStorageQuery(query))
	if err != nil {
		return nil, err
	}

	jobs := make([]*Job, 0, len(result))
	for _, item := range result {
		jobs = append(jobs, cloneJob(item))
	}

	return jobs, nil
}

func (storeMapper) Collection() string {
	return StorageCollection()
}

func (storeMapper) ID(entity *Job) string {
	return entity.JobID
}

func (storeMapper) Encode(entity *Job) ([]byte, error) {
	payload := storagePayload{
		Type:        entity.Type,
		JobID:       entity.JobID,
		UserName:    entity.UserName,
		UserID:      entity.UserID,
		Container:   entity.Container,
		Host:        entity.Host,
		AgentTaskID: entity.AgentTaskID,
		Status:      entity.Status,
		Error:       entity.Error,
		Duration:    entity.Duration,
		Timeout:     entity.Timeout,
		StartTime:   entity.StartTime,
		EndTime:     entity.EndTime,
		Args:        entity.Args,
		Results:     entity.Results,
		LastUpdate:  entity.LastUpdate,
		PrivateData: entity.PrivateData,
	}

	return json.Marshal(payload)
}

func (storeMapper) Decode(data []byte) (*Job, error) {
	var payload storagePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}

	return &Job{
		Type:        payload.Type,
		JobID:       payload.JobID,
		UserName:    payload.UserName,
		UserID:      payload.UserID,
		Container:   payload.Container,
		Host:        payload.Host,
		AgentTaskID: payload.AgentTaskID,
		Status:      payload.Status,
		Error:       payload.Error,
		Duration:    payload.Duration,
		Timeout:     payload.Timeout,
		StartTime:   payload.StartTime,
		EndTime:     payload.EndTime,
		Args:        payload.Args,
		Results:     payload.Results,
		LastUpdate:  payload.LastUpdate,
		PrivateData: payload.PrivateData,
	}, nil
}

func (storeMapper) Fields(entity *Job) (map[string]any, error) {
	return map[string]any{
		"user_id":    entity.UserID,
		"container":  entity.Container,
		"host":       entity.Host,
		"status":     string(entity.Status),
		"type":       entity.Type,
		"start_time": entity.StartTime,
	}, nil
}

func (storeMapper) Indexes() []driver.Index {
	names := []string{
		"user_id",
		"container",
		"host",
		"status",
		"type",
		"start_time",
	}
	indexes := make([]driver.Index, 0, len(names))
	for _, name := range names {
		indexes = append(indexes, driver.Index{Field: name})
	}
	return indexes
}

func toStorageQuery(q *JobQuery) driver.Query {
	filters := make([]driver.Filter, 0, 6)
	if q.UserID != "" && !q.IsAdmin {
		filters = append(filters, driver.Filter{Field: "user_id", Op: driver.OpEq, Value: q.UserID})
	}
	if q.Container != "" {
		filters = append(filters, driver.Filter{Field: "container", Op: driver.OpEq, Value: q.Container})
	}
	if q.Host != "" {
		filters = append(filters, driver.Filter{Field: "host", Op: driver.OpEq, Value: q.Host})
	}
	if q.Status != "" {
		filters = append(filters, driver.Filter{Field: "status", Op: driver.OpEq, Value: q.Status})
	}
	if q.Type != "" {
		filters = append(filters, driver.Filter{Field: "type", Op: driver.OpEq, Value: q.Type})
	}

	return driver.Query{
		Filters: filters,
	}
}

func cloneJob(entity *Job) *Job {
	cloned := *entity
	cloned.PrivateData = cloneJobPrivateData(entity.PrivateData)
	return &cloned
}

func cloneJobPrivateData(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}

	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
