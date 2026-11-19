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
package localfile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sync"
	"sync/atomic"

	"github.com/ccfos/huatuo/internal/filerotate"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/storage/driver"
)

// Storage appends records to local files. It is bound to one collection by Init.
type Storage struct {
	lock         sync.Mutex
	files        map[string]io.Writer
	writerCache  sync.Map
	closed       atomic.Bool
	path         string
	rotationSize int
	maxRotation  int
}

// ErrClosed is returned by Save once the backend has been closed, so a record
// is not appended after shutdown.
var ErrClosed = errors.New("storage: localfile backend closed")

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
		files:        make(map[string]io.Writer),
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

// Close closes every rotator this backend opened and marks the backend
// closed, so a later Save reports ErrClosed instead of reopening the file.
//
// Close is idempotent: the store closes the same backend both when Init fails
// and at shutdown, and the second call must not report an error.
func (b *Storage) Close(_ context.Context) error {
	if !b.closed.CompareAndSwap(false, true) {
		return nil
	}

	b.lock.Lock()
	defer b.lock.Unlock()

	var errs []error
	b.writerCache.Range(func(name, w any) bool {
		closer, ok := w.(io.Closer)
		if !ok {
			return true
		}
		if err := closer.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close localfile writer %q: %w", name, err))
		}
		return true
	})

	return errors.Join(errs...)
}

func (b *Storage) newFileWriter(filename string) io.Writer {
	fp := path.Join(b.path, filename)

	fileWriter, ok := b.writerCache.Load(fp)
	if !ok {
		fileWriter = filerotate.NewFileRotator(fp, b.maxRotation, b.rotationSize)
		b.writerCache.Store(fp, fileWriter)
	}

	b.files[filename] = fileWriter.(io.Writer)
	return b.files[filename]
}

func (b *Storage) writerByName(name string) (io.Writer, error) {
	if b.closed.Load() {
		return nil, ErrClosed
	}

	if fileWriter, ok := b.files[name]; ok {
		return fileWriter, nil
	}

	b.lock.Lock()
	defer b.lock.Unlock()

	if b.closed.Load() {
		return nil, ErrClosed
	}

	// Double-check after acquiring lock
	if fileWriter, ok := b.files[name]; ok {
		return fileWriter, nil
	}

	if _, err := os.Stat(b.path); os.IsNotExist(err) {
		if mkdirErr := os.MkdirAll(b.path, 0o755); mkdirErr != nil {
			return nil, mkdirErr
		}
	}

	return b.newFileWriter(name), nil
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
	if err := json.Indent(&buf, data, "", "\t"); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
