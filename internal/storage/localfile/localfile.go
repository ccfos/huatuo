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

package localfile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"sync"

	"huatuo-bamai/internal/filerotate"
	"huatuo-bamai/internal/storage/types"
)

type StorageClient struct {
	lock         sync.Mutex
	files        map[string]io.Writer
	path         string
	rotationSize int
	maxRotation  int
}

var fileWriterMap sync.Map

func NewStorageClient(path string, maxRotation, rotationSize int) (*StorageClient, error) {
	return &StorageClient{
		path:         path,
		maxRotation:  maxRotation,
		rotationSize: rotationSize,
		files:        make(map[string]io.Writer),
	}, nil
}

// Write the document data into local file.
func (s *StorageClient) Write(doc *types.Document) error {
	buffer := &bytes.Buffer{}
	encoder := json.NewEncoder(buffer)

	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "\t")

	if err := encoder.Encode(doc); err != nil {
		return fmt.Errorf("json Marshal by %s: %w", doc.TracerName, err)
	}

	return s.write(doc.TracerName, buffer.Bytes())
}

// newFileWriter create a file rotator
func (s *StorageClient) newFileWriter(filename string) io.Writer {
	filepath := path.Join(s.path, filename)

	writer, ok := fileWriterMap.Load(filepath)
	if !ok {
		writer = filerotate.NewFileRotator(filepath, s.maxRotation, s.rotationSize)
		fileWriterMap.Store(filepath, writer)
	}

	s.files[filename] = writer.(io.Writer)
	return s.files[filename]
}

func (s *StorageClient) writerByName(name string) io.Writer {
	if writer, ok := s.files[name]; ok {
		return writer
	}

	s.lock.Lock()
	defer s.lock.Unlock()

	if _, err := os.Stat(s.path); os.IsNotExist(err) {
		_ = os.MkdirAll(s.path, 0o755)
	}

	return s.newFileWriter(name)
}

func (s *StorageClient) write(name string, content []byte) error {
	_, err := s.writerByName(name).Write(content)
	return err
}
