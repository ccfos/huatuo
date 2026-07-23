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

// Package sqlite implements a storage backend that persists records to SQLite
// using json_extract-based querying.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"huatuo-bamai/internal/storage/driver"
)

// Storage stores records in SQLite. It is bound to one table by Init.
type Storage struct {
	db    *sql.DB
	table string
}

var _ driver.Backend = (*Storage)(nil)

func init() {
	driver.RegisterBackend("sqlite", func(cfg *driver.Config) (driver.Backend, error) {
		return NewBackend(cfg.SQLiteDSN)
	})
}

// NewBackend creates a SQLite backend.
func NewBackend(dsn string) (*Storage, error) {
	if dsn == "" {
		return nil, fmt.Errorf("sqlite backend: dsn is empty")
	}

	db, err := openDB(dsn)
	if err != nil {
		return nil, err
	}
	return &Storage{db: db}, nil
}

// Close closes the SQLite database.
func (s *Storage) Close(_ context.Context) error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Storage) Init(ctx context.Context, collection string, indexes []driver.Index) error {
	if err := validateIdentifier(collection); err != nil {
		return err
	}
	for _, idx := range indexes {
		if err := validateIdentifier(idx.Field); err != nil {
			return err
		}
	}

	s.table = collection

	createTableSQL := fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
	id TEXT PRIMARY KEY,
	data BLOB NOT NULL,
	fields TEXT NOT NULL
)`, quoteIdentifier(s.table))
	if _, err := s.db.ExecContext(driver.WithContext(ctx), createTableSQL); err != nil {
		return fmt.Errorf("sqlite backend init table %s: %w", s.table, err)
	}

	for _, idx := range indexes {
		createIndexSQL := fmt.Sprintf(
			`CREATE INDEX IF NOT EXISTS %s ON %s(json_extract(fields, '%s'))`,
			quoteIdentifier("idx_"+s.table+"_"+idx.Field),
			quoteIdentifier(s.table),
			jsonPath(idx.Field),
		)
		if _, err := s.db.ExecContext(driver.WithContext(ctx), createIndexSQL); err != nil {
			return fmt.Errorf("sqlite backend init index %s.%s: %w", s.table, idx.Field, err)
		}
	}
	return nil
}

func (s *Storage) Save(ctx context.Context, rec driver.Record) error {
	fieldsJSON, err := normalizedFieldsJSON(rec.Fields)
	if err != nil {
		return err
	}

	saveSQL := fmt.Sprintf(
		`INSERT OR REPLACE INTO %s (id, data, fields) VALUES (?, ?, ?)`,
		quoteIdentifier(s.table),
	)
	if _, err := s.db.ExecContext(driver.WithContext(ctx), saveSQL, rec.ID, rec.Data, fieldsJSON); err != nil {
		return fmt.Errorf("sqlite backend save into %s: %w", s.table, err)
	}
	return nil
}

// Create inserts rec without replacing an existing ID.
func (s *Storage) Create(ctx context.Context, rec driver.Record) error {
	fieldsJSON, err := normalizedFieldsJSON(rec.Fields)
	if err != nil {
		return err
	}

	createSQL := fmt.Sprintf(
		`INSERT INTO %s (id, data, fields) VALUES (?, ?, ?) ON CONFLICT(id) DO NOTHING`,
		quoteIdentifier(s.table),
	)
	result, err := s.db.ExecContext(driver.WithContext(ctx), createSQL, rec.ID, rec.Data, fieldsJSON)
	if err != nil {
		return fmt.Errorf("sqlite backend create in %s: %w", s.table, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite backend create rows affected: %w", err)
	}
	if rows == 0 {
		return driver.ErrAlreadyExists
	}
	return nil
}

func normalizedFieldsJSON(fields map[string]any) (string, error) {
	normalized := make(map[string]any, len(fields))
	for k, v := range fields {
		normalized[k] = driver.NormalizeValue(v)
	}
	fieldsJSON, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("sqlite backend marshal fields: %w", err)
	}
	return string(fieldsJSON), nil
}

func (s *Storage) Get(ctx context.Context, id string) (driver.Record, error) {
	querySQL := fmt.Sprintf(
		`SELECT id, data, fields FROM %s WHERE id = ?`,
		quoteIdentifier(s.table),
	)

	var (
		rec        driver.Record
		fieldsJSON []byte
	)
	err := s.db.QueryRowContext(driver.WithContext(ctx), querySQL, id).Scan(&rec.ID, &rec.Data, &fieldsJSON)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return driver.Record{}, driver.ErrNotFound
		}
		return driver.Record{}, fmt.Errorf("sqlite backend get from %s: %w", s.table, err)
	}

	rec.Fields, err = decodeFields(fieldsJSON)
	if err != nil {
		return driver.Record{}, err
	}
	return rec, nil
}

func (s *Storage) Delete(ctx context.Context, id string) error {
	deleteSQL := fmt.Sprintf(`DELETE FROM %s WHERE id = ?`, quoteIdentifier(s.table))
	if _, err := s.db.ExecContext(driver.WithContext(ctx), deleteSQL, id); err != nil {
		return fmt.Errorf("sqlite backend delete from %s: %w", s.table, err)
	}
	return nil
}

func (s *Storage) Query(ctx context.Context, q driver.Query) ([]driver.Record, error) {
	querySQL, args, err := buildSelectSQL(s.table, q)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(driver.WithContext(ctx), querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite backend query %s: %w", s.table, err)
	}
	defer rows.Close()

	records := make([]driver.Record, 0)
	for rows.Next() {
		var (
			rec        driver.Record
			fieldsJSON []byte
		)
		if err := rows.Scan(&rec.ID, &rec.Data, &fieldsJSON); err != nil {
			return nil, fmt.Errorf("sqlite backend scan record from %s: %w", s.table, err)
		}
		rec.Fields, err = decodeFields(fieldsJSON)
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite backend iterate %s: %w", s.table, err)
	}
	return records, nil
}

func (s *Storage) Count(ctx context.Context, q driver.Query) (int64, error) {
	countSQL, args, err := buildCountSQL(s.table, q)
	if err != nil {
		return 0, err
	}

	var count int64
	if err := s.db.QueryRowContext(driver.WithContext(ctx), countSQL, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("sqlite backend count %s: %w", s.table, err)
	}
	return count, nil
}

func (s *Storage) Values(ctx context.Context, field string, q driver.Query, size int) ([]string, error) {
	if err := validateIdentifier(field); err != nil {
		return nil, err
	}

	valuesSQL, args, err := buildValuesSQL(s.table, field, q, size)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(driver.WithContext(ctx), valuesSQL, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite backend values %s.%s: %w", s.table, field, err)
	}
	defer rows.Close()

	terms := make([]string, 0, size)
	for rows.Next() {
		var value any
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("sqlite backend scan values from %s.%s: %w", s.table, field, err)
		}
		terms = append(terms, driver.StringValue(value))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite backend iterate values %s.%s: %w", s.table, field, err)
	}
	return terms, nil
}

func decodeFields(data []byte) (map[string]any, error) {
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	fields := make(map[string]any)
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, fmt.Errorf("sqlite backend decode fields: %w", err)
	}
	return fields, nil
}
