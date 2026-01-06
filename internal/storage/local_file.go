// Copyright 2025 The HuaTuo Authors
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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"sync"

	rotator "huatuo-bamai/internal/file_rotator"
)

type localFileStorage struct {
	fileLock          sync.Mutex
	files             map[string]io.Writer
	localPath         string
	localRotationSize int
	localMaxRotation  int
}

var fileWriterMap sync.Map

func newLocalFileStorage(path string, maxRotation, rotationSize int) (*localFileStorage, error) {
	return &localFileStorage{
		localPath:         path,
		localMaxRotation:  maxRotation,
		localRotationSize: rotationSize,
		files:             make(map[string]io.Writer),
	}, nil
}

// Write the document data into local file.
func (f *localFileStorage) Write(doc *document) error {
	buffer := &bytes.Buffer{}
	encoder := json.NewEncoder(buffer)

	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "\t")

	if err := encoder.Encode(doc); err != nil {
		return fmt.Errorf("json Marshal by %s: %w", doc.TracerName, err)
	}

	return f.write(doc.TracerName, buffer.Bytes())
}

// newFileWriter create a file rotator
func (f *localFileStorage) newFileWriter(filename string) io.Writer {
	filepath := path.Join(f.localPath, filename)

	writer, ok := fileWriterMap.Load(filepath)
	if !ok {
		writer = rotator.NewSizeRotator(filepath, f.localMaxRotation, f.localRotationSize)
		fileWriterMap.Store(filepath, writer)
	}

	f.files[filename] = writer.(io.Writer)
	return f.files[filename]
}

func (f *localFileStorage) writerByName(name string) io.Writer {
	if writer, ok := f.files[name]; ok {
		return writer
	}

	f.fileLock.Lock()
	defer f.fileLock.Unlock()

	if _, err := os.Stat(f.localPath); os.IsNotExist(err) {
		_ = os.MkdirAll(f.localPath, 0o755)
	}

	return f.newFileWriter(name)
}

func (f *localFileStorage) write(name string, content []byte) error {
	_, err := f.writerByName(name).Write(content)
	return err
}
