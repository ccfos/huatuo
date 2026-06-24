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
	"io"
	"os"
	"path"
	"sync"

	"huatuo-bamai/internal/filerotate"
	"huatuo-bamai/internal/storage/driver"
)

// Storage appends records to local files. It is bound to one collection by Init.
type Storage struct {
	lock         sync.Mutex
	files        map[string]io.Writer
	writerCache  sync.Map
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
		files:        make(map[string]io.Writer),
	}
}

func (b *Storage) Init(_ context.Context, _ string, _ []driver.Index) error {
	return nil
}

func (b *Storage) Save(_ context.Context, rec driver.Record) error {
	filename := tracerFilename(rec)
	if filename == "" {
		return driver.ErrInvalidField
	}

	data, err := formatDocumentJSON(rec.Data)
	if err != nil {
		data = rec.Data
	}
	_, err = b.writerByName(filename).Write(data)
	return err
}

func (b *Storage) Get(context.Context, string) (driver.Record, error) {
	return driver.Record{}, driver.ErrUnsupported
}

func (b *Storage) Delete(context.Context, string) error {
	return driver.ErrUnsupported
}

func (b *Storage) Query(context.Context, driver.Query) ([]driver.Record, error) {
	return nil, driver.ErrUnsupported
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

func (b *Storage) writerByName(name string) io.Writer {
	if fileWriter, ok := b.files[name]; ok {
		return fileWriter
	}

	b.lock.Lock()
	defer b.lock.Unlock()

	if _, err := os.Stat(b.path); os.IsNotExist(err) {
		_ = os.MkdirAll(b.path, 0o755)
	}

	return b.newFileWriter(name)
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
