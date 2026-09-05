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

	Status    Status          `json:"status"`
	Terminal  *TerminalResult `json:"terminal,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	StartedAt time.Time       `json:"started_at,omitempty"`
	EndedAt   time.Time       `json:"ended_at,omitempty"`

	PendingDeadline         time.Time  `json:"pending_deadline,omitempty"`
	ExecutionDeadline       time.Time  `json:"execution_deadline,omitempty"`
	NodeUnavailableDeadline time.Time  `json:"node_unavailable_deadline,omitempty"`
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
	var data []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT data FROM jobs WHERE id = ?`, jobID).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get job %q: %w", jobID, err)
	}
	decodedJob, err := decodeCurrentJob(jobID, data)
	if err != nil {
		return nil, fmt.Errorf("decode job %q: %w", jobID, err)
	}
	return cloneJob(decodedJob), nil
}

func (s *storageStore) Create(ctx context.Context, job *Job) error {
	record, err := encodeStorageRecord(job)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(
		ctx,
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
	job *Job,
	expectedStatus Status,
) error {
	record, err := encodeStorageRecord(job)
	if err != nil {
		return err
	}

	if !isValidStatus(expectedStatus) {
		return fmt.Errorf("%w: unsupported expected status %q", ErrInvalidQuery, expectedStatus)
	}
	query := `UPDATE jobs SET data = ?, fields = ? WHERE id = ?
		AND json_extract(fields, '$.status') = ?`
	args := []any{record.data, record.fields, record.id, string(expectedStatus)}

	result, err := s.db.ExecContext(ctx, query, args...)
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
	return ErrConflict
}

func (s *storageStore) List(ctx context.Context, query *Query) ([]*Job, error) {
	querySQL, args, err := buildListSQL(query)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()

	capacity := 0
	if query != nil && query.Limit > 0 {
		capacity = query.Limit
	}
	jobs := make([]*Job, 0, capacity)
	for rows.Next() {
		var (
			id   string
			data []byte
		)
		if err := rows.Scan(&id, &data); err != nil {
			return nil, fmt.Errorf("scan job list: %w", err)
		}
		decodedJob, err := decodeCurrentJob(id, data)
		if err != nil {
			return nil, fmt.Errorf("decode job %q: %w", id, err)
		}
		jobs = append(jobs, decodedJob)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate jobs: %w", err)
	}
	return jobs, nil
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
		ctx,
		`DELETE FROM jobs WHERE id IN (
			SELECT id FROM jobs
			WHERE json_extract(fields, '$.status') = ?
			  AND json_extract(fields, '$.ended_at') != ''
			  AND json_extract(fields, '$.ended_at') <= ?
			ORDER BY json_extract(fields, '$.ended_at') ASC, id ASC
			LIMIT ?
		)`,
		string(StatusTerminal),
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

func (s *storageStore) Ping(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping job database: %w", err)
	}
	return nil
}

func (s *storageStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func encodeStorageRecord(job *Job) (storageRecord, error) {
	if err := job.validateStored(); err != nil {
		return storageRecord{}, fmt.Errorf("encode job: %w", err)
	}
	payload := storagePayload{
		SchemaVersion:           currentStorageSchemaVersion,
		ID:                      job.ID,
		Kind:                    job.Kind,
		UserID:                  job.UserID,
		Hostname:                job.Hostname,
		DurationSeconds:         int64(job.Duration / time.Second),
		Scope:                   string(job.Scope),
		ContainerID:             job.ContainerID,
		Spec:                    job.Spec,
		Status:                  job.Status,
		Terminal:                job.Terminal,
		CreatedAt:               job.CreatedAt,
		UpdatedAt:               job.UpdatedAt,
		StartedAt:               job.StartedAt,
		EndedAt:                 job.EndedAt,
		PendingDeadline:         job.PendingDeadline,
		ExecutionDeadline:       job.ExecutionDeadline,
		NodeUnavailableDeadline: job.NodeUnavailableDeadline,
		StopDeadline:            job.StopDeadline,
		StopReason:              job.StopReason,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return storageRecord{}, fmt.Errorf("encode job %q payload: %w", job.ID, err)
	}
	fields, err := json.Marshal(map[string]any{
		"id":           job.ID,
		"user_id":      job.UserID,
		"container_id": job.ContainerID,
		"hostname":     job.Hostname,
		"status":       string(job.Status),
		"kind":         string(job.Kind),
		"subtype":      job.Spec.subtype(job.Kind),
		"created_at":   driver.NormalizeValue(job.CreatedAt),
		"ended_at":     normalizedOptionalTime(job.EndedAt),
	})
	if err != nil {
		return storageRecord{}, fmt.Errorf("encode job %q indexes: %w", job.ID, err)
	}
	return storageRecord{id: job.ID, data: data, fields: string(fields)}, nil
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
	decodedJob := &Job{
		ID:                      payload.ID,
		Kind:                    payload.Kind,
		UserID:                  payload.UserID,
		Hostname:                payload.Hostname,
		Duration:                time.Duration(payload.DurationSeconds) * time.Second,
		Scope:                   observation.Scope(payload.Scope),
		ContainerID:             payload.ContainerID,
		Spec:                    payload.Spec,
		Status:                  payload.Status,
		Terminal:                payload.Terminal,
		CreatedAt:               payload.CreatedAt,
		UpdatedAt:               payload.UpdatedAt,
		StartedAt:               payload.StartedAt,
		EndedAt:                 payload.EndedAt,
		PendingDeadline:         payload.PendingDeadline,
		ExecutionDeadline:       payload.ExecutionDeadline,
		NodeUnavailableDeadline: payload.NodeUnavailableDeadline,
		StopDeadline:            payload.StopDeadline,
		StopReason:              payload.StopReason,
	}
	if err := decodedJob.validateStored(); err != nil {
		return nil, err
	}
	return decodedJob, nil
}

func buildListSQL(query *Query) (string, []any, error) {
	if err := validateQuerySort(query); err != nil {
		return "", nil, err
	}
	whereSQL, args := buildWhereSQL(query)
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

func buildWhereSQL(query *Query) (string, []any) {
	if query == nil {
		return "", nil
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
	return strings.Join(clauses, " AND "), args
}

func validateQuerySort(query *Query) error {
	if query == nil {
		return nil
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
