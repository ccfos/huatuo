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

// Package localfile implements a storage backend that appends records to local
// files with rotation support.
//
// Writers are opened lazily, one per tracer name, and shared by every
// concurrent Save for that name. The backend is a process-wide singleton
// reached from one goroutine per toolstream connection, so all writer
// bookkeeping is guarded by a single lock.
package localfile

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path"
	"sync"

	"github.com/ccfos/huatuo/internal/filerotate"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/storage/driver"
)

// Storage appends records to local files. It is bound to one collection by Init.
type Storage struct {
	// mu guards writers. Every access to writers must happen while holding mu:
	// Save runs on one goroutine per toolstream connection, and an
	// unsynchronized map read that races with a map write makes the Go runtime
	// abort the whole agent process with "concurrent map read and map write".
	mu      sync.RWMutex
	writers map[string]io.Writer

	path         string
	rotationSize int
	maxRotation  int
}

var _ driver.Backend = (*Storage)(nil)

// init registers the localfile backend driver so it is available via
// side-effect import.
func init() {
	driver.RegisterBackend("localfile", func(cfg *driver.Config) (driver.Backend, error) {
		return NewBackend(cfg.LocalFilePath, cfg.LocalFileRotationSize, cfg.LocalFileMaxRotation), nil
	})
}

// NewBackend creates a local file backend.
func NewBackend(path string, rotationSize, maxRotation int) *Storage {
	return &Storage{
		path:         path,
		rotationSize: rotationSize,
		maxRotation:  maxRotation,
		writers:      make(map[string]io.Writer),
	}
}

func (b *Storage) Init(_ context.Context, _ string, _ []driver.Index) error {
	return nil
}

func (b *Storage) Save(
	_ context.Context,
	rec driver.Record,
	options driver.SaveOptions,
) error {
	if options.Mode != driver.SaveModeUpsert || len(options.Conditions) != 0 {
		return driver.ErrUnsupportedOp
	}
	filename := tracerFilename(rec)
	if filename == "" {
		return driver.ErrInvalidField
	}

	data, err := formatDocumentJSON(rec.Data)
	if err != nil {
		log.Warnf("localfile: failed to format JSON for record, writing raw data: %v", err)
		data = rec.Data
	}
	w, err := b.writerByName(filename)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func (b *Storage) Get(context.Context, string) (driver.Record, error) {
	return driver.Record{}, driver.ErrUnsupported
}

func (b *Storage) Delete(context.Context, string) error {
	return driver.ErrUnsupported
}

func (b *Storage) DeleteByQuery(context.Context, driver.DeleteQuery) (int64, error) {
	return 0, driver.ErrUnsupported
}

func (b *Storage) Query(context.Context, driver.Query, func([]driver.Record) error) error {
	return driver.ErrUnsupported
}

func (b *Storage) Count(context.Context, driver.Query) (int64, error) {
	return 0, driver.ErrUnsupported
}

func (b *Storage) Values(context.Context, string, driver.Query, int) ([]string, error) {
	return nil, driver.ErrUnsupported
}

// Close is a no-op: the file rotator flushes on each Write, so there is
// nothing buffered to drain at shutdown.
func (b *Storage) Close(_ context.Context) error {
	return nil
}

// writerByName returns the rotator for name, creating it on first use.
//
// The fast path takes only the read lock so Saves for different tracer names do
// not serialize, but it must still hold a lock: an unlocked read of the writers
// map races with the insert below and crashes the process. The write lock is
// taken only on a cache miss, where the double-check keeps the common case from
// opening the same file twice.
func (b *Storage) writerByName(name string) (io.Writer, error) {
	b.mu.RLock()
	w, ok := b.writers[name]
	b.mu.RUnlock()
	if ok {
		return w, nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	// Re-check under the write lock: another goroutine may have created the
	// rotator while this one waited for the lock.
	if existing, ok := b.writers[name]; ok {
		return existing, nil
	}

	if _, err := os.Stat(b.path); os.IsNotExist(err) {
		if mkdirErr := os.MkdirAll(b.path, 0o755); mkdirErr != nil {
			return nil, mkdirErr
		}
	}

	w = filerotate.NewFileRotator(path.Join(b.path, name), b.maxRotation, b.rotationSize)
	b.writers[name] = w
	return w, nil
}

func tracerFilename(rec driver.Record) string {
	if rec.Fields != nil {
		if name, ok := rec.Fields["tracer_name"].(string); ok {
			return name
		}
	}
	return ""
}

func formatDocumentJSON(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := json.Indent(&buf, data, "", "	"); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
