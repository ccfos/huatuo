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

// Package driver defines the storage abstraction layer: configuration types,
// query DSL, the Backend interface, and the backend driver registry.
package driver

import (
	"context"
	"errors"
	"fmt"
)

// Sentinel errors returned by storage operations.
var (
	ErrNotFound      = errors.New("storage: not found")
	ErrInvalidQuery  = errors.New("storage: invalid query")
	ErrUnsupportedOp = errors.New("storage: unsupported op")
	ErrUnsupported   = errors.New("storage: unsupported")
	ErrInvalidField  = errors.New("storage: invalid field")
	ErrEncodeFailed  = errors.New("storage: encode failed")
	ErrDecodeFailed  = errors.New("storage: decode failed")
	ErrAlreadyExists = errors.New("storage: already exists")

	// ErrNegativePagination is returned when Limit or Offset is negative.
	ErrNegativePagination = fmt.Errorf("%w: limit and offset must be non-negative", ErrInvalidQuery)
	// ErrNegativeSize is returned when a Terms size is negative.
	ErrNegativeSize = fmt.Errorf("%w: size must be non-negative", ErrInvalidQuery)
	// ErrInRequiresSlice is returned when an OpIn filter value is not a slice or array.
	ErrInRequiresSlice = fmt.Errorf("%w: in operator requires a slice or array value", ErrInvalidQuery)
	// ErrInRequiresNonEmpty is returned when an OpIn filter value is an empty slice.
	ErrInRequiresNonEmpty = fmt.Errorf("%w: in operator requires at least one value", ErrInvalidQuery)
)

// Config contains backend selection and backend-specific settings.
type Config struct {
	Driver string

	SQLiteDSN string

	LocalFilePath         string
	LocalFileRotationSize int
	LocalFileMaxRotation  int

	ESAddresses []string
	ESUsername  string
	ESPassword  string
	ESIndex     string
}

// Op is a storage query operator.
type Op string

const (
	// OpEq matches values equal to the filter value.
	OpEq Op = "eq"
	// OpNe matches values not equal to the filter value.
	OpNe Op = "ne"
	// OpGt matches values greater than the filter value.
	OpGt Op = "gt"
	// OpGte matches values greater than or equal to the filter value.
	OpGte Op = "gte"
	// OpLt matches values less than the filter value.
	OpLt Op = "lt"
	// OpLte matches values less than or equal to the filter value.
	OpLte Op = "lte"
	// OpIn matches values contained in the filter value.
	OpIn Op = "in"
)

// Filter describes one field predicate in a query.
type Filter struct {
	Field string
	Op    Op
	Value any
}

// Sort describes one field sort in a query.
type Sort struct {
	Field string
	Desc  bool
}

// Query describes filters, ordering, and pagination.
type Query struct {
	Filters []Filter
	Sorts   []Sort
	Limit   int
	Offset  int
}

// Record is the backend-neutral persisted representation.
type Record struct {
	ID     string
	Data   []byte
	Fields map[string]any
}

// Index declares one queryable field.
type Index struct {
	Field string
}

// Mapper converts domain values of type T to and from the storage representation.
type Mapper[T any] interface {
	ID(entity T) string
	Encode(entity T) ([]byte, error)
	Decode(data []byte) (T, error)
	Fields(entity T) (map[string]any, error)
	Indexes() []Index
}

// Backend is implemented by storage backends. A backend instance is bound to
// one collection by Init; subsequent calls operate on that collection.
//
// Close releases backend resources and flushes any buffered writes; backends
// with async write paths (e.g. Elasticsearch bulk) rely on Close to land
// pending records before exit. Implementations must tolerate being closed
// once and never reused; calling Save after Close is undefined.
type Backend interface {
	Init(ctx context.Context, collection string, indexes []Index) error
	Save(ctx context.Context, rec Record) error
	Get(ctx context.Context, id string) (Record, error)
	Delete(ctx context.Context, id string) error
	Query(ctx context.Context, q Query) ([]Record, error)
	Count(ctx context.Context, q Query) (int64, error)
	Values(ctx context.Context, field string, q Query, size int) ([]string, error)
	Close(ctx context.Context) error
}

// Creator is implemented by backends that support insert-only writes.
type Creator interface {
	Create(ctx context.Context, rec Record) error
}
