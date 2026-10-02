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
	ErrConflict      = errors.New("storage: conflict")

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

// DeleteQuery selects records for synchronous bulk deletion. Limit zero
// deletes every matching record; a positive limit bounds the deleted count.
type DeleteQuery struct {
	Filters []Filter
	Limit   int
}

// Record is the backend-neutral persisted representation.
type Record struct {
	ID     string
	Data   []byte
	Fields map[string]any
}

// SaveMode selects the write precondition applied by a backend.
type SaveMode uint8

const (
	// SaveModeUpsert creates or replaces a record without a precondition.
	SaveModeUpsert SaveMode = iota
	// SaveModeCreateOnly creates a record only when its ID does not exist.
	SaveModeCreateOnly
	// SaveModeConditional updates a record only when all Conditions match.
	SaveModeConditional
)

// SaveOptions describes write preconditions and visibility requirements.
type SaveOptions struct {
	Mode              SaveMode
	Conditions        []Filter
	WaitForVisibility bool
}

// Index declares one queryable field.
type Index struct {
	Field string
}

// Mapper converts domain values of type T to and from the storage representation.
type Mapper[T any] interface {
	ID(entity T) string
	Encode(entity T) ([]byte, error)
	Decode(record Record) (T, error)
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
	Save(ctx context.Context, rec Record, options SaveOptions) error
	Get(ctx context.Context, id string) (Record, error)
	Delete(ctx context.Context, id string) error
	DeleteByQuery(ctx context.Context, query DeleteQuery) (int64, error)
	Query(ctx context.Context, q Query) ([]Record, error)
	Count(ctx context.Context, q Query) (int64, error)
	Values(ctx context.Context, field string, q Query, size int) ([]string, error)
	Close(ctx context.Context) error
}

// Pinger verifies that a backend can serve requests.
type Pinger interface {
	Ping(ctx context.Context) error
}
