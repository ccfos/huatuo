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
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"huatuo-bamai/internal/storage/driver"
	"huatuo-bamai/pkg/observation"

	_ "modernc.org/sqlite"
)

const (
	defaultJobsDBPath           = "jobs.db"
	currentStorageSchemaVersion = 1
)

var storageFieldExpressions = map[string]string{
	"id":           "id",
	"user_id":      "json_extract(fields, '$.user_id')",
	"container_id": "json_extract(fields, '$.container_id')",
	"hostname":     "json_extract(fields, '$.hostname')",
	"status":       "json_extract(fields, '$.status')",
	"kind":         "json_extract(fields, '$.kind')",
	"subtype":      "json_extract(fields, '$.subtype')",
	"created_at":   "json_extract(fields, '$.created_at')",
	"ended_at":     "json_extract(fields, '$.ended_at')",
}

type storageStore struct {
	db *sql.DB
}

type storagePayload struct {
	SchemaVersion int `json:"schema_version"`

	ID              string `json:"id"`
	Kind            Kind   `json:"kind"`
	UserID          string `json:"user_id"`
	Hostname        string `json:"hostname"`
	DurationSeconds int64  `json:"duration_seconds"`
	Scope           string `json:"scope"`
	ContainerID     string `json:"container_id,omitempty"`
	Spec            Spec   `json:"spec"`

	Status    Status           `json:"status"`
	Failure   *TerminalFailure `json:"failure,omitempty"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
	StartedAt time.Time        `json:"started_at,omitempty"`
	EndedAt   time.Time        `json:"ended_at,omitempty"`

	StartAttemptedAt        time.Time  `json:"start_attempted_at,omitempty"`
	PendingDeadline         time.Time  `json:"pending_deadline,omitempty"`
	ExecutionDeadline       time.Time  `json:"execution_deadline,omitempty"`
	NodeUnavailableSince    time.Time  `json:"node_unavailable_since,omitempty"`
	NodeUnavailableDeadline time.Time  `json:"node_unavailable_deadline,omitempty"`
	StopRequestedAt         time.Time  `json:"stop_requested_at,omitempty"`
	StopDeadline            time.Time  `json:"stop_deadline,omitempty"`
	StopReason              StopReason `json:"stop_reason,omitempty"`
}

type storageRecord struct {
	id     string
	data   []byte
	fields string
}

func newStore(ctx context.Context, dsn string) (Store, error) {
	if dsn == "" {
		dsn = defaultJobsDBPath
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open job database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(time.Hour)
	db.SetConnMaxIdleTime(30 * time.Minute)

	store := &storageStore{db: db}
	if err := store.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *storageStore) Get(ctx context.Context, jobID string) (*Job, error) {
	if jobID == "" {
		return nil, fmt.Errorf("%w: job ID is required", ErrInvalidQuery)
	}

	var data []byte
	err := s.db.QueryRowContext(contextOrBackground(ctx),
		`SELECT data FROM jobs WHERE id = ?`, jobID).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get job %q: %w", jobID, err)
	}
	jobEntity, err := decodeCurrentJob(jobID, data)
	if err != nil {
		return nil, fmt.Errorf("decode job %q: %w", jobID, err)
	}
	return cloneJob(jobEntity), nil
}

func (s *storageStore) Create(ctx context.Context, jobEntity *Job) error {
	record, err := encodeStorageRecord(jobEntity)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(
		contextOrBackground(ctx),
		`INSERT INTO jobs (id, data, fields) VALUES (?, ?, ?)
		 ON CONFLICT(id) DO NOTHING`,
		record.id,
		record.data,
		record.fields,
	)
	if err != nil {
		return fmt.Errorf("create job %q: %w", record.id, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("create job %q: determine rows affected: %w", record.id, err)
	}
	if rows == 0 {
		return ErrAlreadyExists
	}
	return nil
}

func (s *storageStore) Save(
	ctx context.Context,
	jobEntity *Job,
	expectedStatuses ...Status,
) error {
	record, err := encodeStorageRecord(jobEntity)
	if err != nil {
		return err
	}

	query := `UPDATE jobs SET data = ?, fields = ? WHERE id = ?`
	args := []any{record.data, record.fields, record.id}
	if len(expectedStatuses) > 0 {
		placeholders := make([]string, len(expectedStatuses))
		for i, status := range expectedStatuses {
			if !isValidStatus(status) {
				return fmt.Errorf("%w: unsupported expected status %q", ErrInvalidQuery, status)
			}
			placeholders[i] = "?"
			args = append(args, string(status))
		}
		query += " AND json_extract(fields, '$.status') IN (" +
			strings.Join(placeholders, ", ") + ")"
	}

	result, err := s.db.ExecContext(contextOrBackground(ctx), query, args...)
	if err != nil {
		return fmt.Errorf("save job %q: %w", record.id, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("save job %q: determine rows affected: %w", record.id, err)
	}
	if rows != 0 {
		return nil
	}
	if len(expectedStatuses) > 0 {
		return ErrConflict
	}
	return ErrNotFound
}

func (s *storageStore) List(ctx context.Context, query *Query) ([]*Job, error) {
	querySQL, args, err := buildListSQL(query)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(contextOrBackground(ctx), querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()

	jobs := make([]*Job, 0)
	for rows.Next() {
		var (
			id   string
			data []byte
		)
		if err := rows.Scan(&id, &data); err != nil {
			return nil, fmt.Errorf("scan job list: %w", err)
		}
		jobEntity, err := decodeCurrentJob(id, data)
		if err != nil {
			return nil, fmt.Errorf("decode job %q: %w", id, err)
		}
		jobs = append(jobs, jobEntity)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate jobs: %w", err)
	}
	return jobs, nil
}

func (s *storageStore) Count(ctx context.Context, query *Query) (int64, error) {
	whereSQL, args, err := buildWhereSQL(query)
	if err != nil {
		return 0, err
	}
	querySQL := `SELECT COUNT(*) FROM jobs`
	if whereSQL != "" {
		querySQL += " WHERE " + whereSQL
	}

	var count int64
	if err := s.db.QueryRowContext(contextOrBackground(ctx), querySQL, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count jobs: %w", err)
	}
	return count, nil
}

func (s *storageStore) DeleteTerminalBefore(
	ctx context.Context,
	endedBefore time.Time,
	limit int,
) (int64, error) {
	if endedBefore.IsZero() {
		return 0, fmt.Errorf("%w: cleanup boundary is required", ErrInvalidQuery)
	}
	if limit <= 0 || limit > 1000 {
		return 0, fmt.Errorf("%w: cleanup limit must be between 1 and 1000", ErrInvalidQuery)
	}

	result, err := s.db.ExecContext(
		contextOrBackground(ctx),
		`DELETE FROM jobs WHERE id IN (
			SELECT id FROM jobs
			WHERE json_extract(fields, '$.status') IN (?, ?, ?, ?)
			  AND json_extract(fields, '$.ended_at') != ''
			  AND json_extract(fields, '$.ended_at') <= ?
			ORDER BY json_extract(fields, '$.ended_at') ASC, id ASC
			LIMIT ?
		)`,
		string(StatusCompleted),
		string(StatusFailed),
		string(StatusStopped),
		string(StatusOutcomeUnknown),
		driver.NormalizeValue(endedBefore),
		limit,
	)
	if err != nil {
		return 0, fmt.Errorf("delete terminal jobs: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete terminal jobs: determine rows affected: %w", err)
	}
	return deleted, nil
}

func (s *storageStore) Close(_ context.Context) error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func encodeStorageRecord(jobEntity *Job) (storageRecord, error) {
	if err := jobEntity.validate(); err != nil {
		return storageRecord{}, fmt.Errorf("encode job: %w", err)
	}
	payload := storagePayload{
		SchemaVersion:           currentStorageSchemaVersion,
		ID:                      jobEntity.ID,
		Kind:                    jobEntity.Kind,
		UserID:                  jobEntity.UserID,
		Hostname:                jobEntity.Hostname,
		DurationSeconds:         int64(jobEntity.Duration / time.Second),
		Scope:                   string(jobEntity.Scope),
		ContainerID:             jobEntity.ContainerID,
		Spec:                    jobEntity.Spec,
		Status:                  jobEntity.Status,
		Failure:                 jobEntity.Failure,
		CreatedAt:               jobEntity.CreatedAt,
		UpdatedAt:               jobEntity.UpdatedAt,
		StartedAt:               jobEntity.StartedAt,
		EndedAt:                 jobEntity.EndedAt,
		StartAttemptedAt:        jobEntity.StartAttemptedAt,
		PendingDeadline:         jobEntity.PendingDeadline,
		ExecutionDeadline:       jobEntity.ExecutionDeadline,
		NodeUnavailableSince:    jobEntity.NodeUnavailableSince,
		NodeUnavailableDeadline: jobEntity.NodeUnavailableDeadline,
		StopRequestedAt:         jobEntity.StopRequestedAt,
		StopDeadline:            jobEntity.StopDeadline,
		StopReason:              jobEntity.StopReason,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return storageRecord{}, fmt.Errorf("encode job %q payload: %w", jobEntity.ID, err)
	}
	fields, err := json.Marshal(map[string]any{
		"id":           jobEntity.ID,
		"user_id":      jobEntity.UserID,
		"container_id": jobEntity.ContainerID,
		"hostname":     jobEntity.Hostname,
		"status":       string(jobEntity.Status),
		"kind":         string(jobEntity.Kind),
		"subtype":      jobEntity.Spec.subtype(jobEntity.Kind),
		"created_at":   driver.NormalizeValue(jobEntity.CreatedAt),
		"ended_at":     normalizedOptionalTime(jobEntity.EndedAt),
	})
	if err != nil {
		return storageRecord{}, fmt.Errorf("encode job %q indexes: %w", jobEntity.ID, err)
	}
	return storageRecord{id: jobEntity.ID, data: data, fields: string(fields)}, nil
}

func decodeCurrentJob(rowID string, data []byte) (*Job, error) {
	var payload storagePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}
	if payload.SchemaVersion != currentStorageSchemaVersion {
		return nil, fmt.Errorf("unsupported schema version %d", payload.SchemaVersion)
	}
	if payload.ID != rowID {
		return nil, fmt.Errorf("payload ID %q does not match row ID", payload.ID)
	}
	jobEntity := &Job{
		ID:                      payload.ID,
		Kind:                    payload.Kind,
		UserID:                  payload.UserID,
		Hostname:                payload.Hostname,
		Duration:                time.Duration(payload.DurationSeconds) * time.Second,
		Scope:                   observation.Scope(payload.Scope),
		ContainerID:             payload.ContainerID,
		Spec:                    payload.Spec,
		Status:                  payload.Status,
		Failure:                 payload.Failure,
		CreatedAt:               payload.CreatedAt,
		UpdatedAt:               payload.UpdatedAt,
		StartedAt:               payload.StartedAt,
		EndedAt:                 payload.EndedAt,
		StartAttemptedAt:        payload.StartAttemptedAt,
		PendingDeadline:         payload.PendingDeadline,
		ExecutionDeadline:       payload.ExecutionDeadline,
		NodeUnavailableSince:    payload.NodeUnavailableSince,
		NodeUnavailableDeadline: payload.NodeUnavailableDeadline,
		StopRequestedAt:         payload.StopRequestedAt,
		StopDeadline:            payload.StopDeadline,
		StopReason:              payload.StopReason,
	}
	if err := jobEntity.validate(); err != nil {
		return nil, err
	}
	return jobEntity, nil
}

func buildListSQL(query *Query) (string, []any, error) {
	if err := validateQuery(query); err != nil {
		return "", nil, err
	}
	whereSQL, args, err := buildWhereSQL(query)
	if err != nil {
		return "", nil, err
	}
	querySQL := `SELECT id, data FROM jobs`
	if whereSQL != "" {
		querySQL += " WHERE " + whereSQL
	}

	sortField, descending := querySort(query)
	querySQL += " ORDER BY " + storageFieldExpressions[sortField]
	if descending {
		querySQL += " DESC"
	} else {
		querySQL += " ASC"
	}
	if sortField != "id" {
		querySQL += ", id"
		if descending {
			querySQL += " DESC"
		} else {
			querySQL += " ASC"
		}
	}
	if query != nil && query.Limit > 0 {
		querySQL += " LIMIT ?"
		args = append(args, query.Limit)
	}
	if query != nil && query.Offset > 0 {
		if query.Limit == 0 {
			querySQL += " LIMIT -1"
		}
		querySQL += " OFFSET ?"
		args = append(args, query.Offset)
	}
	return querySQL, args, nil
}

func buildWhereSQL(query *Query) (string, []any, error) {
	if err := validateQuery(query); err != nil {
		return "", nil, err
	}
	if query == nil {
		return "", nil, nil
	}

	clauses := make([]string, 0, 8)
	args := make([]any, 0, 8)
	appendEqual := func(field, value string) {
		if value == "" {
			return
		}
		clauses = append(clauses, storageFieldExpressions[field]+" = ?")
		args = append(args, value)
	}
	appendEqual("id", query.ID)
	if !query.IsAdmin {
		appendEqual("user_id", query.UserID)
	}
	appendEqual("container_id", query.ContainerID)
	appendEqual("hostname", query.Hostname)

	appendIn := func(field string, values []string) {
		if len(values) == 0 {
			return
		}
		placeholders := make([]string, len(values))
		for i, value := range values {
			placeholders[i] = "?"
			args = append(args, value)
		}
		clauses = append(clauses, storageFieldExpressions[field]+" IN ("+
			strings.Join(placeholders, ", ")+")")
	}
	statuses := make([]string, len(query.Statuses))
	for i, status := range query.Statuses {
		statuses[i] = string(status)
	}
	appendIn("status", statuses)
	kinds := make([]string, len(query.Kinds))
	for i, kind := range query.Kinds {
		kinds[i] = string(kind)
	}
	appendIn("kind", kinds)
	appendIn("subtype", query.Subtypes)
	return strings.Join(clauses, " AND "), args, nil
}

func validateQuery(query *Query) error {
	if query == nil {
		return nil
	}
	if query.Limit < 0 || query.Limit > 1000 {
		return fmt.Errorf("%w: limit must be between 0 and 1000", ErrInvalidQuery)
	}
	if query.Offset < 0 {
		return fmt.Errorf("%w: offset must not be negative", ErrInvalidQuery)
	}
	for _, status := range query.Statuses {
		if !isValidStatus(status) {
			return fmt.Errorf("%w: unsupported status %q", ErrInvalidQuery, status)
		}
	}
	for _, kind := range query.Kinds {
		if kind != KindProfiling && kind != KindTracing {
			return fmt.Errorf("%w: unsupported kind %q", ErrInvalidQuery, kind)
		}
	}
	field, _ := querySort(query)
	if _, ok := storageFieldExpressions[field]; !ok {
		return fmt.Errorf("%w: unsupported sort field %q", ErrInvalidQuery, field)
	}
	return nil
}

func querySort(query *Query) (string, bool) {
	if query == nil || query.Sort == "" {
		return "created_at", true
	}
	field := query.Sort
	descending := strings.HasPrefix(field, "-")
	field = strings.TrimPrefix(field, "-")
	return field, descending
}

func normalizedOptionalTime(value time.Time) any {
	if value.IsZero() {
		return ""
	}
	return driver.NormalizeValue(value)
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
