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
	"fmt"

	"huatuo-bamai/internal/storage/driver"
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

	return NewStore(ctx, cfg.Driver, backend, collection, mapper)
}

// NewStore validates that backend and mapper are non-nil, verifies the collection
// name, and calls backend.Init to create tables and indexes.
func NewStore[T any](ctx context.Context, name string, backend driver.Backend, collection string, mapper driver.Mapper[T]) (*Store[T], error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if backend == nil {
		return nil, fmt.Errorf("storage: backend is nil")
	}
	if mapper == nil {
		return nil, fmt.Errorf("storage: mapper is nil")
	}

	if collection == "" {
		return nil, fmt.Errorf("storage: collection is empty")
	}

	for _, idx := range mapper.Indexes() {
		if idx.Field == "" {
			return nil, fmt.Errorf("%w: empty index field", driver.ErrInvalidField)
		}
	}

	if err := backend.Init(ctx, collection, mapper.Indexes()); err != nil {
		return nil, err
	}

	return &Store[T]{
		Name:    name,
		backend: backend,
		mapper:  mapper,
	}, nil
}

// Save persists v; returns ErrInvalidField if the ID is empty.
func (s *Store[T]) Save(ctx context.Context, v T) error {
	rec, err := s.record(v)
	if err != nil {
		return err
	}
	return s.backend.Save(driver.WithContext(ctx), rec)
}

// Create persists v only when its ID does not already exist.
func (s *Store[T]) Create(ctx context.Context, v T) error {
	creator, ok := s.backend.(driver.Creator)
	if !ok {
		return driver.ErrUnsupportedOp
	}
	rec, err := s.record(v)
	if err != nil {
		return err
	}
	return creator.Create(driver.WithContext(ctx), rec)
}

func (s *Store[T]) record(v T) (driver.Record, error) {
	fields, err := s.mapper.Fields(v)
	if err != nil {
		return driver.Record{}, err
	}

	data, err := s.mapper.Encode(v)
	if err != nil {
		return driver.Record{}, fmt.Errorf("%w: %w", driver.ErrEncodeFailed, err)
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
	rec, err := s.backend.Get(driver.WithContext(ctx), id)
	if err != nil {
		var zero T
		return zero, err
	}
	return s.mapper.Decode(rec.Data)
}

// Delete removes an object from storage by ID.
func (s *Store[T]) Delete(ctx context.Context, id string) error {
	return s.backend.Delete(driver.WithContext(ctx), id)
}

// Close releases backend resources and flushes any pending writes. The store
// must not be used after Close returns.
func (s *Store[T]) Close(ctx context.Context) error {
	return s.backend.Close(driver.WithContext(ctx))
}

// Query returns objects matching q; all filter and sort fields must be registered indexes.
func (s *Store[T]) Query(ctx context.Context, q driver.Query) ([]T, error) {
	if err := s.validateQuery(q); err != nil {
		return nil, err
	}

	records, err := s.backend.Query(driver.WithContext(ctx), q)
	if err != nil {
		return nil, err
	}

	values := make([]T, 0, len(records))
	for _, rec := range records {
		value, decodeErr := s.mapper.Decode(rec.Data)
		if decodeErr != nil {
			return nil, fmt.Errorf("%w: %w", driver.ErrDecodeFailed, decodeErr)
		}
		values = append(values, value)
	}

	return values, nil
}

// Count returns the number of objects matching the given query.
func (s *Store[T]) Count(ctx context.Context, q driver.Query) (int64, error) {
	if err := s.validateQuery(q); err != nil {
		return 0, err
	}

	return s.backend.Count(driver.WithContext(ctx), q)
}

// Values returns up to size distinct values for field, filtered by q.
func (s *Store[T]) Values(ctx context.Context, field string, q driver.Query, size int) ([]string, error) {
	if size < 0 {
		return nil, driver.ErrNegativeSize
	}
	if err := s.validateQuery(q); err != nil {
		return nil, err
	}

	return s.backend.Values(driver.WithContext(ctx), field, q, size)
}

// validateQuery checks that limit and offset are non-negative.
func (s *Store[T]) validateQuery(q driver.Query) error {
	if q.Limit < 0 || q.Offset < 0 {
		return driver.ErrNegativePagination
	}

	return nil
}
