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

package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"huatuo-bamai/internal/storage"
	"huatuo-bamai/internal/watch"
)

// Store fans tracing events out to configured persistence backends and watchers.
type Store struct {
	backends []*storage.Store[*Document]
	hub      *watch.Hub[*Document]
}

// New creates a tracing store over the supplied persistence backends.
func New(backends []*storage.Store[*Document]) *Store {
	return &Store{
		backends: backends,
		hub:      watch.NewHub[*Document](),
	}
}

// Save publishes and asynchronously persists one tracing document.
func (s *Store) Save(document *Document) error {
	if s == nil {
		return errors.New("tracing store is required")
	}
	if document == nil {
		return errors.New("tracing document is required")
	}
	document.UploadedTimestamp = time.Now().UTC()
	if err := document.validate(); err != nil {
		return err
	}
	s.hub.Notify(document)
	var errs []error
	for _, backend := range s.backends {
		if backend == nil {
			continue
		}
		if err := backend.Save(context.Background(), document); err != nil {
			errs = append(errs, fmt.Errorf("save tracing document to %q: %w", backend.Name, err))
		}
	}
	return errors.Join(errs...)
}

// Subscribe registers a watcher for tracing documents.
func (s *Store) Subscribe() (<-chan *Document, func()) {
	return s.hub.Subscribe()
}

// Close flushes and releases every configured backend once.
func (s *Store) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	var errs []error
	for _, backend := range s.backends {
		if backend == nil {
			continue
		}
		if err := backend.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("close tracing store %q: %w", backend.Name, err))
		}
	}
	return errors.Join(errs...)
}
