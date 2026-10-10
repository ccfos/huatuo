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
	"sync"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/storage"
	"github.com/ccfos/huatuo/internal/storage/driver"
	"github.com/ccfos/huatuo/internal/timeutil"
	"github.com/ccfos/huatuo/internal/watch"
)

// Store fans tracing events out to configured persistence backends and watchers.
type Store struct {
	backends []*storage.Store[*Document]
	hub      *watch.Hub[*Document]

	// Cancellation can outlive a backend save; Close waits before releasing it.
	mu      sync.Mutex
	active  int
	closing bool
	closed  bool
	idle    chan struct{}
}

var errStoreClosing = errors.New("tracing store is closing")

// Config contains optional persistence backend settings.
type Config struct {
	Elasticsearch *ElasticsearchConfig
	LocalFile     *LocalFileConfig
}

// ElasticsearchConfig contains Elasticsearch backend settings.
type ElasticsearchConfig struct {
	Addresses []string
	Username  string
	Password  string
	Index     string
}

// LocalFileConfig contains local file backend settings.
type LocalFileConfig struct {
	Path            string
	RotationSizeMiB int
	MaxRotatedFiles int
}

func (c Config) validate() error {
	if c.Elasticsearch != nil && len(c.Elasticsearch.Addresses) == 0 {
		return errors.New("tracing store: Elasticsearch addresses are required")
	}
	if c.LocalFile == nil {
		return nil
	}
	if c.LocalFile.Path == "" {
		return errors.New("tracing store: local file path is required")
	}
	if c.LocalFile.RotationSizeMiB <= 0 {
		return errors.New("tracing store: local file rotation size must be greater than zero MiB")
	}
	if c.LocalFile.MaxRotatedFiles <= 0 {
		return errors.New("tracing store: maximum rotated local files must be greater than zero")
	}
	return nil
}

// NewFromConfig creates a tracing store and its configured persistence backends.
func NewFromConfig(
	ctx context.Context,
	config Config,
) (_ *Store, returnedErr error) {
	if err := config.validate(); err != nil {
		return nil, err
	}

	backends := make([]*storage.Store[*Document], 0, 2)
	defer func() {
		if returnedErr != nil {
			returnedErr = errors.Join(returnedErr, closeBackends(context.Background(), backends))
		}
	}()

	if config.Elasticsearch != nil {
		backendConfig := config.Elasticsearch
		backend, err := storage.NewFromConfig[*Document](ctx, &driver.Config{
			Driver:      "elasticsearch",
			ESAddresses: backendConfig.Addresses,
			ESUsername:  backendConfig.Username,
			ESPassword:  backendConfig.Password,
			ESIndex:     backendConfig.Index,
		}, Collection, mapper{})
		if err != nil {
			return nil, fmt.Errorf("new tracing document store (elasticsearch): %w", err)
		}
		backends = append(backends, backend)
	}

	if config.LocalFile != nil {
		backendConfig := config.LocalFile
		backend, err := storage.NewFromConfig[*Document](ctx, &driver.Config{
			Driver:                "localfile",
			LocalFilePath:         backendConfig.Path,
			LocalFileRotationSize: backendConfig.RotationSizeMiB,
			LocalFileMaxRotation:  backendConfig.MaxRotatedFiles,
		}, Collection, mapper{})
		if err != nil {
			return nil, fmt.Errorf("new tracing document store (localfile): %w", err)
		}
		backends = append(backends, backend)
	}

	return &Store{
		backends: backends,
		hub:      watch.NewHub[*Document](),
	}, nil
}

func closeBackends(ctx context.Context, backends []*storage.Store[*Document]) error {
	var errs []error
	for _, backend := range backends {
		if err := backend.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("close tracing store %q: %w", backend.Name, err))
		}
	}
	return errors.Join(errs...)
}

// Save publishes and asynchronously persists one tracing document.
func (s *Store) Save(document *Document) error {
	return s.SaveContext(context.Background(), document)
}

// SaveContext publishes a document and passes cancellation to persistence.
func (s *Store) SaveContext(ctx context.Context, document *Document) error {
	if s == nil {
		return errors.New("tracing store is required")
	}
	if document == nil {
		return errors.New("tracing document is required")
	}
	if err := s.beginSave(); err != nil {
		return err
	}
	defer s.endSave()
	if ctx == nil {
		ctx = context.Background()
	}
	document.UploadedTimestamp = timeutil.Now()
	if err := document.validate(); err != nil {
		return err
	}
	s.hub.Notify(document)
	var errs []error
	for _, backend := range s.backends {
		err := backend.Save(ctx, document, driver.SaveOptions{})
		log.Debugf("save tracing document backend=%s tracer_id=%s tracer_name=%s hostname=%s "+
			"uploaded_timestamp=%v tracer_data=%+v error=%v",
			backend.Name, document.TracerID, document.TracerName, document.Hostname,
			document.UploadedTimestamp, document.TracerData, err)
		if err != nil {
			errs = append(errs, fmt.Errorf("save tracing document to %q: %w", backend.Name, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Store) beginSave() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.closed {
		return errStoreClosing
	}
	if s.active == 0 {
		s.idle = make(chan struct{})
	}
	s.active++
	return nil
}

func (s *Store) endSave() {
	s.mu.Lock()
	s.active--
	if s.active == 0 {
		close(s.idle)
	}
	s.mu.Unlock()
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
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closing = true
	var idle <-chan struct{}
	if s.active != 0 {
		idle = s.idle
	}
	s.mu.Unlock()
	if idle != nil {
		select {
		case <-idle:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	err := closeBackends(ctx, s.backends)
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return err
}
