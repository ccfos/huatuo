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

package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/ccfos/huatuo/internal/storage/driver"
)

// Store is a generic, backend-agnostic CRUD abstraction; a Mapper[T] handles
// encoding, decoding, and index declarations for the domain type T.
type Store[T any] struct {
	Name    string
	backend driver.Backend
	mapper  driver.Mapper[T]
}

// NewFromConfig creates a Store from Config and Mapper.
func NewFromConfig[T any](ctx context.Context, cfg *driver.Config, collection string, mapper driver.Mapper[T]) (*Store[T], error) {
	backend, err := driver.NewBackend(cfg)
	if err != nil {
		return nil, err
	}

	store, err := NewStore(ctx, cfg.Driver, backend, collection, mapper)
	if err == nil {
		return store, nil
	}
	// This constructor owns backend, so cleanup must survive a canceled init.
	cleanupCtx := context.WithoutCancel(ctx)
	if closeErr := backend.Close(cleanupCtx); closeErr != nil {
		return nil, errors.Join(
			err,
			fmt.Errorf("close storage backend %q: %w", cfg.Driver, closeErr),
		)
	}
	return nil, err
}

// NewStore validates that backend and mapper are non-nil, verifies the collection
// name, and calls backend.Init to create tables and indexes.
func NewStore[T any](ctx context.Context, name string, backend driver.Backend, collection string, mapper driver.Mapper[T]) (*Store[T], error) {
	if backend == nil {
		return nil, fmt.Errorf("storage: backend is nil")
	}
	if mapper == nil {
		return nil, fmt.Errorf("storage: mapper is nil")
	}

	if collection == "" {
		return nil, fmt.Errorf("storage: collection is empty")
	}

	indexes := mapper.Indexes()
	for _, idx := range indexes {
		if idx.Field == "" {
			return nil, fmt.Errorf("%w: empty index field", driver.ErrInvalidField)
		}
	}

	if err := backend.Init(ctx, collection, indexes); err != nil {
		return nil, err
	}

	return &Store[T]{
		Name:    name,
		backend: backend,
		mapper:  mapper,
	}, nil
}

// Save persists v according to options; it returns ErrInvalidField if the ID is empty.
func (s *Store[T]) Save(ctx context.Context, v T, options driver.SaveOptions) error {
	if err := validateSaveOptions(options); err != nil {
		return err
	}
	rec, err := s.record(v)
	if err != nil {
		return err
	}
	return s.backend.Save(ctx, rec, options)
}

func (s *Store[T]) record(v T) (driver.Record, error) {
	data, err := s.mapper.Encode(v)
	if err != nil {
		return driver.Record{}, fmt.Errorf("%w: %w", driver.ErrEncodeFailed, err)
	}

	fields, err := s.mapper.Fields(v)
	if err != nil {
		return driver.Record{}, err
	}

	rec := driver.Record{
		ID:     s.mapper.ID(v),
		Data:   data,
		Fields: fields,
	}
	if rec.ID == "" {
		return driver.Record{}, fmt.Errorf("%w: empty id", driver.ErrInvalidField)
	}
	return rec, nil
}

// Get retrieves the object with the given id; returns ErrNotFound when not found.
func (s *Store[T]) Get(ctx context.Context, id string) (T, error) {
	rec, err := s.backend.Get(ctx, id)
	if err != nil {
		var zero T
		return zero, err
	}
	return s.mapper.Decode(rec)
}

// Delete removes an object from storage by ID.
func (s *Store[T]) Delete(ctx context.Context, id string) error {
	return s.backend.Delete(ctx, id)
}

// DeleteByQuery synchronously deletes records matching query. The returned
// count may be non-zero with an error when the backend completes only part of
// the deletion.
func (s *Store[T]) DeleteByQuery(ctx context.Context, query driver.DeleteQuery) (int64, error) {
	if len(query.Filters) == 0 {
		return 0, fmt.Errorf(
			"%w: query deletion requires at least one filter",
			driver.ErrInvalidQuery,
		)
	}
	if query.Limit < 0 {
		return 0, fmt.Errorf(
			"%w: delete limit must be non-negative",
			driver.ErrInvalidQuery,
		)
	}
	return s.backend.DeleteByQuery(ctx, query)
}

// Close releases backend resources and flushes any pending writes. The store
// must not be used after Close returns.
func (s *Store[T]) Close(ctx context.Context) error {
	return s.backend.Close(ctx)
}

// Ping verifies that the backend can serve requests.
func (s *Store[T]) Ping(ctx context.Context) error {
	pinger, ok := s.backend.(driver.Pinger)
	if !ok {
		return fmt.Errorf(
			"%w: storage backend %q does not support ping",
			driver.ErrUnsupportedOp,
			s.Name,
		)
	}
	return pinger.Ping(ctx)
}

// Query decodes and delivers one batch at a time so consumers need not retain
// the entire result. A consumer error stops the read without rolling back prior batches.
func (s *Store[T]) Query(ctx context.Context, q driver.Query, consume func([]T) error) error {
	if err := validateQuery(q); err != nil {
		return err
	}
	if consume == nil {
		return fmt.Errorf("%w: query consumer is required", driver.ErrInvalidQuery)
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	return s.backend.Query(ctx, q, func(records []driver.Record) error {
		values := make([]T, 0, len(records))
		for i := range records {
			if err := ctx.Err(); err != nil {
				return err
			}
			value, err := s.mapper.Decode(records[i])
			if err != nil {
				return fmt.Errorf("%w: %w", driver.ErrDecodeFailed, err)
			}
			values = append(values, value)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return consume(values)
	})
}

// Count uses only q.Filters so pagination cannot truncate the matching total.
func (s *Store[T]) Count(ctx context.Context, q driver.Query) (int64, error) {
	return s.backend.Count(ctx, q)
}

// Values uses only q.Filters; size controls distinct values independently of record pagination.
func (s *Store[T]) Values(ctx context.Context, field string, q driver.Query, size int) ([]string, error) {
	if size < 0 {
		return nil, driver.ErrNegativeSize
	}

	return s.backend.Values(ctx, field, q, size)
}
