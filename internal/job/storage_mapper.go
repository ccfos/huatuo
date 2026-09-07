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
	"fmt"
	"strconv"
	"time"

	"huatuo-bamai/internal/storage/driver"
	"huatuo-bamai/pkg/observation"
)

const currentStorageSchemaVersion = 1

type recordMapper struct{}

var _ driver.Mapper[*Job] = recordMapper{}

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

func (recordMapper) ID(job *Job) string {
	if job == nil {
		return ""
	}
	return job.ID
}

func (recordMapper) Encode(job *Job) ([]byte, error) {
	if err := job.validateStored(); err != nil {
		return nil, fmt.Errorf("encode job: %w", err)
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
		return nil, fmt.Errorf("encode job %q payload: %w", job.ID, err)
	}
	return data, nil
}

func (recordMapper) Decode(record driver.Record) (*Job, error) {
	var payload storagePayload
	if err := json.Unmarshal(record.Data, &payload); err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}
	if payload.SchemaVersion != currentStorageSchemaVersion {
		return nil, fmt.Errorf("unsupported schema version %d", payload.SchemaVersion)
	}
	if payload.ID != record.ID {
		return nil, fmt.Errorf("payload ID %q does not match row ID %q", payload.ID, record.ID)
	}
	revision, err := decodeStorageRevision(record.Fields)
	if err != nil {
		return nil, fmt.Errorf("field revision: %w", err)
	}
	job := &Job{
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
		revision:                revision,
	}
	if err := job.validateStored(); err != nil {
		return nil, err
	}
	return job, nil
}

func (recordMapper) Fields(job *Job) (map[string]any, error) {
	return map[string]any{
		"id":           job.ID,
		"user_id":      job.UserID,
		"container_id": job.ContainerID,
		"hostname":     job.Hostname,
		"status":       string(job.Status),
		"kind":         string(job.Kind),
		"subtype":      job.Spec.subtype(job.Kind),
		"created_at":   driver.NormalizeValue(job.CreatedAt),
		"ended_at":     normalizedOptionalTime(job.EndedAt),
		"revision":     job.revision,
	}, nil
}

func (recordMapper) Indexes() []driver.Index {
	return []driver.Index{
		{Field: "user_id"},
		{Field: "container_id"},
		{Field: "hostname"},
		{Field: "status"},
		{Field: "kind"},
		{Field: "subtype"},
		{Field: "created_at"},
		{Field: "ended_at"},
		{Field: "revision"},
	}
}

func decodeStorageRevision(fields map[string]any) (int64, error) {
	value, ok := fields["revision"]
	if !ok {
		return 0, errors.New("value is required")
	}
	var (
		revision int64
		err      error
	)
	switch value := value.(type) {
	case json.Number:
		revision, err = value.Int64()
	case int64:
		revision = value
	case int:
		revision = int64(value)
	case float64:
		revision, err = strconv.ParseInt(strconv.FormatFloat(value, 'f', -1, 64), 10, 64)
	default:
		err = fmt.Errorf("must be an integer, got %T", value)
	}
	if err != nil {
		return 0, fmt.Errorf("must be an integer: %w", err)
	}
	if revision <= 0 {
		return 0, errors.New("value is required")
	}
	return revision, nil
}

func normalizedOptionalTime(value time.Time) any {
	if value.IsZero() {
		return ""
	}
	return driver.NormalizeValue(value)
}
