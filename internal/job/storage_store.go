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
	"errors"
	"fmt"
	"time"

	"huatuo-bamai/internal/storage"
	"huatuo-bamai/internal/storage/driver"
)

type storageStore struct {
	store *storage.Store[*Job]
}

type storagePayload struct {
	Type         JobType          `json:"type"`
	ID           string           `json:"id"`
	Username     string           `json:"username"`
	UserID       string           `json:"user_id"`
	ContainerID  string           `json:"container_id"`
	Hostname     string           `json:"hostname"`
	AgentTaskID  string           `json:"agent_task_id"`
	Status       JobStatus        `json:"status"`
	ErrorMessage string           `json:"error_message,omitempty"`
	Duration     int              `json:"duration"`
	TraceTimeout int              `json:"trace_timeout"`
	CreatedAt    time.Time        `json:"created_at"`
	FinishedAt   time.Time        `json:"finished_at"`
	AgentTask    AgentTaskRequest `json:"agent_task"`
	Result       Result           `json:"result,omitempty"`
	UpdatedAt    time.Time        `json:"updated_at"`
	PrivateData  json.RawMessage  `json:"private_data,omitempty"`
}

type storeMapper struct{}

func storageCollection() string {
	return "jobs"
}

func storageFields(entity *Job) map[string]any {
	return map[string]any{
		"id":           entity.ID,
		"user_id":      entity.UserID,
		"container_id": entity.ContainerID,
		"hostname":     entity.Hostname,
		"status":       string(entity.Status),
		"type":         entity.Type,
		"created_at":   entity.CreatedAt,
		"finished_at":  entity.FinishedAt,
	}
}

func storageIndexes() []string {
	return []string{
		"user_id",
		"id",
		"container_id",
		"hostname",
		"status",
		"type",
		"created_at",
		"finished_at",
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
		storageCollection(),
		storeMapper{},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize job backend: %w", err)
	}

	return &storageStore{store: store}, nil
}

func (s *storageStore) Get(ctx context.Context, jobID string) (*Job, error) {
	entity, err := s.store.Get(ctx, jobID)
	if err != nil {
		return nil, err
	}

	return cloneJob(entity), nil
}

func (s *storageStore) Save(ctx context.Context, jobEntity *Job) error {
	if jobEntity == nil {
		return fmt.Errorf("job store: job is nil")
	}

	return s.store.Save(ctx, jobEntity)
}

func (s *storageStore) Create(ctx context.Context, jobEntity *Job) error {
	if jobEntity == nil {
		return errors.New("job store: job is nil")
	}
	return s.store.Create(ctx, jobEntity)
}

func (s *storageStore) Delete(ctx context.Context, jobID string) error {
	return s.store.Delete(ctx, jobID)
}

func (s *storageStore) List(ctx context.Context, query *JobQuery) ([]*Job, error) {
	if err := validateJobQuery(query); err != nil {
		return nil, err
	}
	result, err := s.store.Query(ctx, toStorageQuery(query))
	if err != nil {
		return nil, err
	}

	jobs := make([]*Job, 0, len(result))
	for _, item := range result {
		jobs = append(jobs, cloneJob(item))
	}

	return jobs, nil
}

func (s *storageStore) Count(ctx context.Context, query *JobQuery) (int64, error) {
	if err := validateJobQuery(query); err != nil {
		return 0, err
	}
	return s.store.Count(ctx, toStorageQuery(query))
}

func validateJobQuery(query *JobQuery) error {
	if query == nil {
		return nil
	}
	if query.Limit < 0 || query.Limit > 1000 {
		return fmt.Errorf("%w: limit must be between 0 and 1000", ErrInvalidQuery)
	}
	if query.Offset < 0 {
		return fmt.Errorf("%w: offset must not be negative", ErrInvalidQuery)
	}
	field := query.Sort
	if field != "" && field[0] == '-' {
		field = field[1:]
	}
	switch field {
	case "", "id", "created_at", "finished_at", "hostname", "container_id", "status", "type":
		return nil
	default:
		return fmt.Errorf("%w: unsupported sort field %q", ErrInvalidQuery, field)
	}
}

func (s *storageStore) Close(ctx context.Context) error {
	return s.store.Close(ctx)
}

func (storeMapper) ID(entity *Job) string {
	return entity.ID
}

func (storeMapper) Encode(entity *Job) ([]byte, error) {
	payload := storagePayload{
		Type:         entity.Type,
		ID:           entity.ID,
		Username:     entity.Username,
		UserID:       entity.UserID,
		ContainerID:  entity.ContainerID,
		Hostname:     entity.Hostname,
		AgentTaskID:  entity.AgentTaskID,
		Status:       entity.Status,
		ErrorMessage: entity.ErrorMessage,
		Duration:     entity.Duration,
		TraceTimeout: entity.TraceTimeout,
		CreatedAt:    entity.CreatedAt,
		FinishedAt:   entity.FinishedAt,
		AgentTask:    entity.AgentTask,
		Result:       entity.Result,
		UpdatedAt:    entity.UpdatedAt,
		PrivateData:  entity.PrivateData,
	}

	return json.Marshal(payload)
}

func (storeMapper) Decode(data []byte) (*Job, error) {
	var payload storagePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}

	return &Job{
		Type:         payload.Type,
		ID:           payload.ID,
		Username:     payload.Username,
		UserID:       payload.UserID,
		ContainerID:  payload.ContainerID,
		Hostname:     payload.Hostname,
		AgentTaskID:  payload.AgentTaskID,
		Status:       payload.Status,
		ErrorMessage: payload.ErrorMessage,
		Duration:     payload.Duration,
		TraceTimeout: payload.TraceTimeout,
		CreatedAt:    payload.CreatedAt,
		FinishedAt:   payload.FinishedAt,
		AgentTask:    payload.AgentTask,
		Result:       payload.Result,
		UpdatedAt:    payload.UpdatedAt,
		PrivateData:  payload.PrivateData,
	}, nil
}

func (storeMapper) Fields(entity *Job) (map[string]any, error) {
	return storageFields(entity), nil
}

func (storeMapper) Indexes() []driver.Index {
	names := storageIndexes()
	indexes := make([]driver.Index, 0, len(names))
	for _, name := range names {
		indexes = append(indexes, driver.Index{Field: name})
	}
	return indexes
}

func toStorageQuery(q *JobQuery) driver.Query {
	if q == nil {
		q = &JobQuery{}
	}
	filters := make([]driver.Filter, 0, 6)
	if q.UserID != "" && !q.IsAdmin {
		filters = append(filters, driver.Filter{Field: "user_id", Op: driver.OpEq, Value: q.UserID})
	}
	if q.ContainerID != "" {
		filters = append(filters, driver.Filter{Field: "container_id", Op: driver.OpEq, Value: q.ContainerID})
	}
	if q.Hostname != "" {
		filters = append(filters, driver.Filter{Field: "hostname", Op: driver.OpEq, Value: q.Hostname})
	}
	if len(q.Statuses) > 0 {
		values := make([]string, len(q.Statuses))
		for i := range q.Statuses {
			values[i] = string(q.Statuses[i])
		}
		filters = append(filters, driver.Filter{Field: "status", Op: driver.OpIn, Value: values})
	} else if q.Status != "" {
		filters = append(filters, driver.Filter{Field: "status", Op: driver.OpEq, Value: q.Status})
	}
	if len(q.Types) == 1 {
		filters = append(filters, driver.Filter{Field: "type", Op: driver.OpEq, Value: string(q.Types[0])})
	} else if len(q.Types) > 1 {
		values := make([]string, len(q.Types))
		for i := range q.Types {
			values[i] = string(q.Types[i])
		}
		filters = append(filters, driver.Filter{Field: "type", Op: driver.OpIn, Value: values})
	}

	query := driver.Query{
		Filters: filters,
		Limit:   q.Limit,
		Offset:  q.Offset,
	}
	field := q.Sort
	desc := false
	if field == "" {
		field = "-created_at"
	}
	if field[0] == '-' {
		desc = true
		field = field[1:]
	}
	query.Sorts = []driver.Sort{{Field: field, Desc: desc}, {Field: "id", Desc: desc}}
	return query
}

func cloneJob(entity *Job) *Job {
	if entity == nil {
		return nil
	}
	cloned := *entity
	cloned.PrivateData = cloneJobPrivateData(entity.PrivateData)
	cloned.AgentTask.TracerArgs = append([]string(nil), entity.AgentTask.TracerArgs...)
	cloned.stopCh = entity.stopCh
	return &cloned
}

func cloneJobPrivateData(input json.RawMessage) json.RawMessage {
	if len(input) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), input...)
}
