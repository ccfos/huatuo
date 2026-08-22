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

// Package doris implements a storage backend on Apache Doris: queries run over
// the MySQL protocol, writes go through batched Stream Load.
package doris

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/storage/driver"
)

// Defaults chosen so a load carries a worthwhile batch without holding many
// multi-megabyte profile documents in memory.
const (
	defaultBatchMaxRows  = 16
	defaultFlushInterval = 5 * time.Second
	defaultBuckets       = 4
	defaultReplicas      = 1

	// batchMaxBytes bounds buffered memory rather than batch size: a single
	// heavy profile can reach several MB, so the row count alone is not a
	// memory bound. Batches normally trip defaultBatchMaxRows first.
	batchMaxBytes = 8 << 20
)

// Group commit modes. Merging loads server-side is what keeps the version
// count sane when many agents write the same table concurrently — client-side
// batching cannot help there, since each agent only holds its own rows.
const (
	// GroupCommitOff gives every load its own transaction and label.
	GroupCommitOff = "off_mode"
	// GroupCommitSync waits for the shared commit, so it costs a full
	// group_commit_interval_ms per load.
	GroupCommitSync = "sync_mode"
	// GroupCommitAsync returns once the rows reach the WAL; they become
	// visible when the shared commit fires.
	GroupCommitAsync = "async_mode"
)

// Storage stores records in Doris. It is bound to one table by Init.
type Storage struct {
	db       *sql.DB
	database string
	username string
	password string
	feAddr   string

	buckets       int
	replicas      int
	batchMaxRows  int
	flushInterval time.Duration

	partitionField  string
	partitionColumn string
	retentionDays   int
	maxRetries      int
	groupCommit     string

	table  string
	loader *streamLoader
	kinds  map[string]driver.Kind
	loc    *time.Location

	mu         sync.Mutex
	batch      []map[string]any
	batchBytes int

	cancel context.CancelFunc
	done   chan struct{}
}

var _ driver.Backend = (*Storage)(nil)

func init() {
	driver.RegisterBackend("doris", func(cfg *driver.Config) (driver.Backend, error) {
		return NewBackend(cfg)
	})
}

// NewBackend creates a Doris backend from cfg.
func NewBackend(cfg *driver.Config) (*Storage, error) {
	if cfg.DorisMySQLAddr == "" {
		return nil, fmt.Errorf("doris backend: MySQLAddr is empty")
	}
	if cfg.DorisHTTPAddr == "" {
		return nil, fmt.Errorf("doris backend: HTTPAddr is empty")
	}
	if cfg.DorisDatabase == "" {
		return nil, fmt.Errorf("doris backend: Database is empty")
	}

	db, err := openDB(cfg)
	if err != nil {
		return nil, err
	}

	s := &Storage{
		db:            db,
		database:      cfg.DorisDatabase,
		username:      cfg.DorisUsername,
		password:      cfg.DorisPassword,
		feAddr:        cfg.DorisHTTPAddr,
		buckets:       orDefault(cfg.DorisBuckets, defaultBuckets),
		replicas:      orDefault(cfg.DorisReplicas, defaultReplicas),
		batchMaxRows:  orDefault(cfg.DorisBatchMaxRows, defaultBatchMaxRows),
		flushInterval: defaultFlushInterval,

		partitionField: cfg.DorisPartitionField,
		retentionDays:  cfg.DorisRetentionDays,
		maxRetries:     cfg.DorisMaxRetries,
		groupCommit:    cfg.DorisGroupCommit,
	}
	if cfg.DorisFlushIntervalSeconds > 0 {
		s.flushInterval = time.Duration(cfg.DorisFlushIntervalSeconds) * time.Second
	}

	return s, nil
}

func orDefault(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func (s *Storage) Init(ctx context.Context, collection string, indexes []driver.Index) error {
	if err := validateIdentifier(collection); err != nil {
		return err
	}
	s.table = collection
	s.kinds = columnKinds(indexes)
	s.loader = newStreamLoader(s.feAddr, s.database, s.table, s.username, s.password,
		s.maxRetries, s.groupCommit)

	ctx = driver.WithContext(ctx)
	loc, err := sessionLocation(ctx, s.db)
	if err != nil {
		return err
	}
	s.loc = loc

	if _, err := s.db.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS "+quote(s.database)); err != nil {
		return fmt.Errorf("doris backend init database %s: %w", s.database, err)
	}

	createSQL, buildErr := buildCreateTableSQL(s.database, s.table, indexes, TableOptions{
		Buckets:        s.buckets,
		Replicas:       s.replicas,
		PartitionField: s.partitionField,
		RetentionDays:  s.retentionDays,
	})
	if buildErr != nil {
		return buildErr
	}
	if s.partitionField != "" {
		if s.partitionColumn, err = resolveColumn(s.partitionField); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, createSQL); err != nil {
		return fmt.Errorf("doris backend init table %s: %w", s.table, err)
	}

	flushCtx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	go s.flushLoop(flushCtx)

	return nil
}

// flushLoop drains partial batches so low-rate writers still land data.
func (s *Storage) flushLoop(ctx context.Context) {
	defer close(s.done)

	ticker := time.NewTicker(s.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.Flush(context.Background()); err != nil {
				log.Errorf("doris backend periodic flush into %s.%s: %v",
					s.database, s.table, err)
			}
		}
	}
}

func (s *Storage) Save(ctx context.Context, rec driver.Record) error {
	row, size, err := s.buildRow(rec)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.batch = append(s.batch, row)
	s.batchBytes += size
	full := len(s.batch) >= s.batchMaxRows || s.batchBytes >= batchMaxBytes
	s.mu.Unlock()

	if !full {
		return nil
	}
	return s.Flush(ctx)
}

func (s *Storage) buildRow(rec driver.Record) (map[string]any, int, error) {
	normalized := make(map[string]any, len(rec.Fields))
	for name, value := range rec.Fields {
		normalized[name] = normalizeValue(value, s.loc)
	}
	fieldsJSON, err := json.Marshal(normalized)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: marshal fields: %w", driver.ErrEncodeFailed, err)
	}

	// fields goes in as an object, not a JSON string, so the VARIANT column
	// parses it into subcolumns instead of storing one opaque scalar.
	row := map[string]any{
		colID:     rec.ID,
		colData:   string(rec.Data),
		colFields: normalized,
	}
	for name, value := range normalized {
		column, err := resolveColumn(name)
		if err != nil {
			return nil, 0, err
		}
		row[column] = driver.StringValue(value)
	}

	return row, len(rec.Data) + len(fieldsJSON), nil
}

// Flush writes any buffered rows. It is safe to call with an empty batch.
func (s *Storage) Flush(ctx context.Context) error {
	s.mu.Lock()
	if len(s.batch) == 0 {
		s.mu.Unlock()
		return nil
	}
	batch := s.batch
	s.batch = nil
	s.batchBytes = 0
	s.mu.Unlock()

	// Retries are exhausted by the time load returns an error, so the batch
	// is dropped rather than retained: holding it would grow without bound
	// while the backend stays unreachable. Say how much was lost.
	if err := s.loader.load(driver.WithContext(ctx), batch); err != nil {
		return fmt.Errorf("doris backend dropped %d rows for %s.%s: %w",
			len(batch), s.database, s.table, err)
	}

	log.Debugf("doris backend flushed %d rows into %s.%s", len(batch), s.database, s.table)
	return nil
}

func (s *Storage) Get(ctx context.Context, id string) (driver.Record, error) {
	querySQL := fmt.Sprintf("SELECT %s, %s, %s FROM %s WHERE %s = ?",
		quote(colID), quote(colData), quote(colFields), qualified(s.database, s.table), quote(colID))

	var (
		rec        driver.Record
		data       sql.NullString
		fieldsJSON sql.NullString
	)
	err := s.db.QueryRowContext(driver.WithContext(ctx), querySQL, id).Scan(&rec.ID, &data, &fieldsJSON)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return driver.Record{}, driver.ErrNotFound
		}
		return driver.Record{}, fmt.Errorf("doris backend get from %s: %w", s.table, err)
	}

	rec.Data = []byte(data.String)
	rec.Fields, err = decodeFields(fieldsJSON.String)
	if err != nil {
		return driver.Record{}, err
	}
	return rec, nil
}

func (s *Storage) Delete(ctx context.Context, id string) error {
	deleteSQL := fmt.Sprintf("DELETE FROM %s WHERE %s = ?", qualified(s.database, s.table), quote(colID))
	if _, err := s.db.ExecContext(driver.WithContext(ctx), deleteSQL, id); err != nil {
		return fmt.Errorf("doris backend delete from %s: %w", s.table, err)
	}
	return nil
}

func (s *Storage) Query(ctx context.Context, q driver.Query) ([]driver.Record, error) {
	querySQL, args, err := buildSelectSQL(s.database, s.table, q, s.loc)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(driver.WithContext(ctx), querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("doris backend query %s: %w", s.table, err)
	}
	defer rows.Close()

	records := make([]driver.Record, 0)
	for rows.Next() {
		var (
			rec        driver.Record
			data       sql.NullString
			fieldsJSON sql.NullString
		)
		if err := rows.Scan(&rec.ID, &data, &fieldsJSON); err != nil {
			return nil, fmt.Errorf("doris backend scan record from %s: %w", s.table, err)
		}
		rec.Data = []byte(data.String)
		if rec.Fields, err = decodeFields(fieldsJSON.String); err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("doris backend iterate %s: %w", s.table, err)
	}
	return records, nil
}

func (s *Storage) Count(ctx context.Context, q driver.Query) (int64, error) {
	countSQL, args, err := buildCountSQL(s.database, s.table, q, s.loc)
	if err != nil {
		return 0, err
	}

	var count int64
	if err := s.db.QueryRowContext(driver.WithContext(ctx), countSQL, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("doris backend count %s: %w", s.table, err)
	}
	return count, nil
}

func (s *Storage) Values(ctx context.Context, field string, q driver.Query, size int) ([]string, error) {
	valuesSQL, args, err := buildValuesSQL(s.database, s.table, field, q, size, s.kinds, s.loc)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(driver.WithContext(ctx), valuesSQL, args...)
	if err != nil {
		return nil, fmt.Errorf("doris backend values %s.%s: %w", s.table, field, err)
	}
	defer rows.Close()

	terms := make([]string, 0, size)
	for rows.Next() {
		var value sql.NullString
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("doris backend scan values from %s.%s: %w", s.table, field, err)
		}
		if value.Valid {
			terms = append(terms, value.String)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("doris backend iterate values %s.%s: %w", s.table, field, err)
	}
	return terms, nil
}

// Close stops the flusher and lands whatever is still buffered.
func (s *Storage) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if s.cancel != nil {
		s.cancel()
		<-s.done
		s.cancel = nil
	}

	var errs []error
	if s.loader != nil {
		if err := s.Flush(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if s.db != nil {
		if err := s.db.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// sessionLocation resolves the zone Doris renders DATETIME values in. The named
// zone is preferred so daylight-saving transitions stay correct; when the name
// is unloadable (a bare offset, or an image without tzdata) the server's own
// offset is measured instead.
func sessionLocation(ctx context.Context, db *sql.DB) (*time.Location, error) {
	var name string
	if err := db.QueryRowContext(ctx, "SELECT @@time_zone").Scan(&name); err != nil {
		return nil, fmt.Errorf("doris backend: read session time zone: %w", err)
	}
	if loc, err := time.LoadLocation(name); err == nil {
		return loc, nil
	}

	var offsetSeconds int64
	if err := db.QueryRowContext(ctx,
		"SELECT TIMESTAMPDIFF(SECOND, UTC_TIMESTAMP(), NOW())").Scan(&offsetSeconds); err != nil {
		return nil, fmt.Errorf("doris backend: measure session time zone offset: %w", err)
	}

	return time.FixedZone(name, int(offsetSeconds)), nil
}

func decodeFields(data string) (map[string]any, error) {
	if data == "" {
		return map[string]any{}, nil
	}
	fields := make(map[string]any)
	if err := json.Unmarshal([]byte(data), &fields); err != nil {
		return nil, fmt.Errorf("%w: doris backend decode fields: %w", driver.ErrDecodeFailed, err)
	}
	return fields, nil
}
