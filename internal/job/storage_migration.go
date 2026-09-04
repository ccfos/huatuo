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
	"strconv"
	"strings"
	"time"

	"huatuo-bamai/pkg/observation"
	"huatuo-bamai/pkg/profiling"
	tracingdomain "huatuo-bamai/pkg/tracing"
)

type legacyStoragePayload struct {
	Type         string                 `json:"type"`
	ID           string                 `json:"id"`
	Username     string                 `json:"username"`
	UserID       string                 `json:"user_id"`
	ContainerID  string                 `json:"container_id"`
	Hostname     string                 `json:"hostname"`
	Status       string                 `json:"status"`
	ErrorMessage string                 `json:"error_message"`
	Duration     int                    `json:"duration"`
	TraceTimeout int                    `json:"trace_timeout"`
	CreatedAt    time.Time              `json:"created_at"`
	FinishedAt   time.Time              `json:"finished_at"`
	AgentTask    legacyAgentTaskRequest `json:"agent_task"`
	UpdatedAt    time.Time              `json:"updated_at"`
	PrivateData  json.RawMessage        `json:"private_data"`
}

type legacyAgentTaskRequest struct {
	TracerName        string   `json:"tracer_name"`
	TraceTimeout      int      `json:"trace_timeout"`
	Duration          int      `json:"duration"`
	ContainerID       string   `json:"container_id"`
	ContainerHostname string   `json:"container_hostname"`
	TracerArgs        []string `json:"tracer_args"`
}

type legacyProfilingPrivateData struct {
	BinaryMatchPath string `json:"binary_match_path"`
	DurationSeconds int64  `json:"duration_seconds"`
	Language        string `json:"language"`
	MemoryMode      string `json:"memory_mode"`
}

type migrationRow struct {
	id   string
	data []byte
}

func (s *storageStore) migrate(ctx context.Context) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin job storage migration: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS jobs (
			id TEXT PRIMARY KEY,
			data BLOB NOT NULL,
			fields TEXT NOT NULL
		)`); err != nil {
		return fmt.Errorf("create job table: %w", err)
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT id, data FROM jobs ORDER BY id`)
	if err != nil {
		return fmt.Errorf("read jobs for migration: %w", err)
	}
	migrationRows := make([]migrationRow, 0)
	for rows.Next() {
		var row migrationRow
		if err = rows.Scan(&row.id, &row.data); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan job for migration: %w", err)
		}
		migrationRows = append(migrationRows, row)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate jobs for migration: %w", err)
	}
	if err = rows.Close(); err != nil {
		return fmt.Errorf("close job migration rows: %w", err)
	}

	for i := range migrationRows {
		row := &migrationRows[i]
		version, versionErr := payloadSchemaVersion(row.data)
		if versionErr != nil {
			return fmt.Errorf("migrate job %q field schema_version: %w", row.id, versionErr)
		}
		switch version {
		case 0:
			migratedJob, migrationErr := migrateLegacyJob(row.id, row.data)
			if migrationErr != nil {
				return fmt.Errorf("migrate job %q: %w", row.id, migrationErr)
			}
			record, encodeErr := encodeStorageRecord(migratedJob)
			if encodeErr != nil {
				return fmt.Errorf("migrate job %q: %w", row.id, encodeErr)
			}
			if _, err = tx.ExecContext(
				ctx,
				`UPDATE jobs SET data = ?, fields = ? WHERE id = ?`,
				record.data,
				record.fields,
				record.id,
			); err != nil {
				return fmt.Errorf("migrate job %q: persist converted record: %w", row.id, err)
			}
		case currentStorageSchemaVersion:
			if _, decodeErr := decodeCurrentJob(row.id, row.data); decodeErr != nil {
				return fmt.Errorf("migrate job %q: %w", row.id, decodeErr)
			}
		default:
			return fmt.Errorf(
				"migrate job %q field schema_version: unsupported version %d",
				row.id,
				version,
			)
		}
	}

	for _, statement := range migrationIndexStatements() {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate job indexes: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit job storage migration: %w", err)
	}
	return nil
}

func payloadSchemaVersion(data []byte) (int, error) {
	var header struct {
		SchemaVersion json.RawMessage `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return 0, fmt.Errorf("decode payload: %w", err)
	}
	if len(header.SchemaVersion) == 0 || string(header.SchemaVersion) == "null" {
		return 0, nil
	}
	var version int
	if err := json.Unmarshal(header.SchemaVersion, &version); err != nil {
		return 0, fmt.Errorf("must be an integer: %w", err)
	}
	return version, nil
}

func migrateLegacyJob(rowID string, data []byte) (*Job, error) {
	var legacy legacyStoragePayload
	if err := json.Unmarshal(data, &legacy); err != nil {
		return nil, fmt.Errorf("decode legacy payload: %w", err)
	}
	if legacy.ID != rowID {
		return nil, fmt.Errorf("field id: payload ID %q does not match row ID", legacy.ID)
	}
	userID := legacy.UserID
	if userID == "" {
		userID = legacy.Username
	}
	if userID == "" {
		return nil, errors.New("field user_id: value is required")
	}
	if legacy.UpdatedAt.IsZero() {
		legacy.UpdatedAt = legacy.CreatedAt
	}

	migratedJob := &Job{
		ID:          rowID,
		UserID:      userID,
		Hostname:    legacy.Hostname,
		ContainerID: legacy.ContainerID,
		Status:      Status(legacy.Status),
		CreatedAt:   legacy.CreatedAt,
		UpdatedAt:   legacy.UpdatedAt,
		EndedAt:     legacy.FinishedAt,
	}
	if migratedJob.ContainerID == "" {
		migratedJob.Scope = observation.ScopeHost
	} else {
		migratedJob.Scope = observation.ScopeContainer
	}

	var err error
	switch legacy.Type {
	case "profiling_cpu":
		migratedJob.Kind = KindProfiling
		migratedJob.Duration, migratedJob.Spec, err = migrateLegacyProfiling(
			profiling.TypeCPU,
			&legacy,
		)
	case "profiling_memory":
		migratedJob.Kind = KindProfiling
		migratedJob.Duration, migratedJob.Spec, err = migrateLegacyProfiling(
			profiling.TypeMemory,
			&legacy,
		)
	case "tracing":
		migratedJob.Kind = KindTracing
		migratedJob.Duration, migratedJob.Spec, err = migrateLegacyTracing(&legacy)
	default:
		err = fmt.Errorf("field type: unsupported value %q", legacy.Type)
	}
	if err != nil {
		return nil, err
	}

	if err := migrateLegacyStatus(migratedJob, &legacy); err != nil {
		return nil, err
	}
	return migratedJob, nil
}

func migrateLegacyProfiling(
	typ profiling.Type,
	legacy *legacyStoragePayload,
) (time.Duration, Spec, error) {
	var privateData legacyProfilingPrivateData
	if len(legacy.PrivateData) != 0 && string(legacy.PrivateData) != "null" {
		if err := json.Unmarshal(legacy.PrivateData, &privateData); err != nil {
			return 0, Spec{}, fmt.Errorf("field private_data: %w", err)
		}
	}

	durationSeconds := privateData.DurationSeconds
	if durationSeconds <= 0 {
		value, ok := legacyFlagValue(legacy.AgentTask.TracerArgs, "--duration")
		if ok {
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil || parsed <= 0 {
				return 0, Spec{}, fmt.Errorf("field agent_task.tracer_args duration: invalid value %q", value)
			}
			durationSeconds = parsed
		}
	}
	if durationSeconds <= 0 && legacy.Duration > 0 {
		durationSeconds = int64(legacy.Duration / 2)
	}
	if durationSeconds <= 0 {
		return 0, Spec{}, errors.New("field duration: profiling duration is required")
	}

	languageValue := privateData.Language
	if languageValue == "" {
		languageValue, _ = legacyFlagValue(legacy.AgentTask.TracerArgs, "-l", "--language")
	}
	language, err := profiling.ParseLanguage(languageValue)
	if err != nil {
		return 0, Spec{}, fmt.Errorf("field private_data.language: %w", err)
	}

	mode := profiling.ModeOnCPU
	if typ == profiling.TypeMemory {
		modeValue := privateData.MemoryMode
		if modeValue == "" {
			modeValue, _ = legacyFlagValue(legacy.AgentTask.TracerArgs, "--memory-mode")
		}
		parsedMode, err := profiling.ParseMode(strings.ToLower(modeValue))
		if err != nil {
			return 0, Spec{}, fmt.Errorf("field private_data.memory_mode: %w", err)
		}
		mode = parsedMode
	}

	profilingSpec := &profiling.Spec{
		Type:            typ,
		Language:        language,
		Mode:            mode,
		BinaryMatchPath: privateData.BinaryMatchPath,
	}
	if profilingSpec.BinaryMatchPath == "" {
		profilingSpec.BinaryMatchPath, _ = legacyFlagValue(
			legacy.AgentTask.TracerArgs,
			"--binary-match-path",
		)
	}
	return time.Duration(durationSeconds) * time.Second, Spec{Profiling: profilingSpec}, nil
}

func migrateLegacyTracing(legacy *legacyStoragePayload) (time.Duration, Spec, error) {
	tracingType, err := legacyTracingType(legacy.AgentTask.TracerName)
	if err != nil {
		return 0, Spec{}, fmt.Errorf("field agent_task.tracer_name: %w", err)
	}
	durationSeconds := legacy.Duration
	if durationSeconds <= 0 {
		durationSeconds = legacy.AgentTask.Duration
	}
	if durationSeconds <= 0 {
		durationSeconds = legacy.TraceTimeout
	}
	if durationSeconds <= 0 {
		durationSeconds = legacy.AgentTask.TraceTimeout
	}
	if durationSeconds <= 0 {
		return 0, Spec{}, errors.New("field duration: tracing duration is required")
	}
	return time.Duration(durationSeconds) * time.Second,
		Spec{Tracing: &tracingdomain.Spec{Type: tracingType}}, nil
}

func migrateLegacyStatus(job *Job, legacy *legacyStoragePayload) error {
	message := strings.TrimSpace(legacy.ErrorMessage)
	switch legacy.Status {
	case "completed":
		job.Status = StatusTerminal
		job.Terminal = &TerminalResult{Outcome: OutcomeCompleted}
	case "failed":
		if message == "" {
			message = "legacy job execution failed"
		}
		job.Status = StatusTerminal
		job.Terminal = &TerminalResult{
			Outcome: OutcomeFailed,
			Reason:  FailureReasonExecutionFailed,
			Message: message,
		}
	case "stopped":
		job.Status = StatusTerminal
		job.Terminal = &TerminalResult{Outcome: OutcomeStopped}
	case "timeout":
		if message == "" {
			message = "job exceeded its execution deadline"
		}
		job.Status = StatusTerminal
		job.Terminal = &TerminalResult{
			Outcome: OutcomeFailed,
			Reason:  FailureReasonExecutionTimedOut,
			Message: message,
		}
	case "pending", "running":
		job.Status = StatusTerminal
		job.Terminal = &TerminalResult{
			Outcome: OutcomeUnknown,
			Reason:  FailureReasonOperationLost,
			Message: "operation state was lost during Task API migration",
		}
	default:
		return fmt.Errorf("field status: unsupported value %q", legacy.Status)
	}
	if job.EndedAt.IsZero() {
		// Legacy active records become terminal during migration and have no finish time.
		job.EndedAt = job.UpdatedAt
	}
	return nil
}

func legacyTracingType(value string) (tracingdomain.Type, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "dropwatch", "networking_drop":
		return tracingdomain.TypeNetworkingDrop, nil
	case "iotracing", "io_tracing":
		return tracingdomain.TypeIO, nil
	case "tcpshark", "tcp_retransmit", "tcp_retransmission":
		return tracingdomain.TypeTCPRetransmit, nil
	default:
		return tracingdomain.TypeUnknown, fmt.Errorf("unsupported value %q", value)
	}
}

func legacyFlagValue(args []string, names ...string) (string, bool) {
	for i := 0; i < len(args); i++ {
		for _, name := range names {
			if args[i] == name && i+1 < len(args) && args[i+1] != "" {
				return args[i+1], true
			}
			prefix := name + "="
			if strings.HasPrefix(args[i], prefix) && len(args[i]) > len(prefix) {
				return strings.TrimPrefix(args[i], prefix), true
			}
		}
	}
	return "", false
}

func migrationIndexStatements() []string {
	return []string{
		`DROP INDEX IF EXISTS idx_jobs_type`,
		`DROP INDEX IF EXISTS idx_jobs_finished_at`,
		`CREATE INDEX IF NOT EXISTS idx_jobs_id ON jobs(id)`,
		`CREATE INDEX IF NOT EXISTS idx_jobs_user_id
		 ON jobs(json_extract(fields, '$.user_id'))`,
		`CREATE INDEX IF NOT EXISTS idx_jobs_container_id
		 ON jobs(json_extract(fields, '$.container_id'))`,
		`CREATE INDEX IF NOT EXISTS idx_jobs_hostname
		 ON jobs(json_extract(fields, '$.hostname'))`,
		`CREATE INDEX IF NOT EXISTS idx_jobs_status
		 ON jobs(json_extract(fields, '$.status'))`,
		`CREATE INDEX IF NOT EXISTS idx_jobs_kind
		 ON jobs(json_extract(fields, '$.kind'))`,
		`CREATE INDEX IF NOT EXISTS idx_jobs_subtype
		 ON jobs(json_extract(fields, '$.subtype'))`,
		`CREATE INDEX IF NOT EXISTS idx_jobs_created_at
		 ON jobs(json_extract(fields, '$.created_at'))`,
		`CREATE INDEX IF NOT EXISTS idx_jobs_ended_at
		 ON jobs(json_extract(fields, '$.ended_at'))`,
	}
}
