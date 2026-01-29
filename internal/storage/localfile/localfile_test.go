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

package localfile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"huatuo-bamai/internal/storage/types"
)

// TestnewLocalFileStorage tests creation of local file storage.
func TestNewLocalFileStorage(t *testing.T) {
	tests := []struct {
		name             string
		dir              string
		maxRotation      int
		rotationSize     int
		wantErr          bool
		wantPath         string
		wantMaxRotation  int
		wantRotationSize int
	}{
		{
			name:             "Basic valid parameters",
			dir:              t.TempDir(),
			maxRotation:      3,
			rotationSize:     1024,
			wantErr:          false,
			wantPath:         "", // will assign dynamically
			wantMaxRotation:  3,
			wantRotationSize: 1024,
		},
		{
			name:             "Larger rotation values",
			dir:              t.TempDir(),
			maxRotation:      10,
			rotationSize:     4096,
			wantErr:          false,
			wantPath:         "",
			wantMaxRotation:  10,
			wantRotationSize: 4096,
		},
		{
			name:             "Zero rotation size",
			dir:              t.TempDir(),
			maxRotation:      2,
			rotationSize:     0,
			wantErr:          false,
			wantPath:         "",
			wantMaxRotation:  2,
			wantRotationSize: 0,
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			storage, err := NewStorageClient(tt.dir, tt.maxRotation, tt.rotationSize)

			// Check error expectation
			if (err != nil) != tt.wantErr {
				t.Fatalf("unexpected error status: got error=%v, wantErr=%v", err, tt.wantErr)
			}
			if err != nil {
				// When error expected, no further checks
				return
			}

			// Assign dynamic directory for checking
			tt.wantPath = tt.dir

			// Validate the returned storage fields
			if storage.path != tt.wantPath {
				t.Errorf("expected path %q, got %q", tt.wantPath, storage.path)
			}
			if storage.maxRotation != tt.wantMaxRotation {
				t.Errorf("expected maxRotation %d, got %d", tt.wantMaxRotation, storage.maxRotation)
			}
			if storage.rotationSize != tt.wantRotationSize {
				t.Errorf("expected rotationSize %d, got %d", tt.wantRotationSize, storage.rotationSize)
			}
		})
	}
}

// TestWrite tests basic Write behavior.
func TestWrite_Success(t *testing.T) {
	tmpDir := t.TempDir()
	storage, _ := NewStorageClient(tmpDir, 3, 1024)

	doc := &types.Document{
		TracerName:        "trace1",
		Hostname:          "host1",
		Region:            "region1",
		ContainerID:       "cid1",
		ContainerHostname: "chost1",
		ContainerType:     "docker",
		ContainerQos:      "gold",
		TracerID:          "tid1",
		TracerData:        "hello",
	}

	if err := storage.Write(doc); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	// Read back the file data
	data, err := os.ReadFile(filepath.Join(tmpDir, doc.TracerName))
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	// Unmarshal into Document struct
	var gotDoc types.Document
	if err := json.Unmarshal(data, &gotDoc); err != nil {
		t.Fatalf("json unmarshal failed: %v", err)
	}

	// Compare struct deeply
	if !reflect.DeepEqual(doc, &gotDoc) {
		t.Errorf("written document does not match original\nwant: %+v\ngot:  %+v", doc, gotDoc)
	}
}

// TestWrite_JSONMarshalError tests the error path when JSON encoding fails.
func TestWrite_JSONMarshalError(t *testing.T) {
	tmpDir := t.TempDir()
	storage, _ := NewStorageClient(tmpDir, 3, 1024)

	// Create a document that cannot be marshaled by encoding/json.
	// Functions are not supported by json.Marshal / Encoder.
	doc := &types.Document{
		TracerName:        "trace-error",
		Hostname:          "host1",
		Region:            "region1",
		ContainerID:       "cid1",
		ContainerHostname: "chost1",
		ContainerType:     "docker",
		ContainerQos:      "gold",
		TracerID:          "tid1",
		TracerData:        func() {}, // force json marshal error
	}

	err := storage.Write(doc)
	if err == nil {
		t.Fatalf("expected Write to fail, got nil error")
	}

	// Verify error message contains tracer name and custom prefix.
	if !strings.Contains(err.Error(), "json Marshal by trace-error") {
		t.Fatalf("unexpected error message: %v", err)
	}

	// Verify the underlying error is a json marshaling error.
	var jsonErr *json.UnsupportedTypeError
	if !errors.As(err, &jsonErr) {
		t.Fatalf("expected wrapped json.UnsupportedTypeError, got %T", err)
	}

	// Verify no file was written.
	if _, statErr := os.Stat(filepath.Join(tmpDir, doc.TracerName)); !os.IsNotExist(statErr) {
		t.Fatalf("file should not be created when json marshal fails")
	}
}

// TestWrite_InOrder verifies that writes to the same file are performed
// in the exact order of Write calls when executed sequentially.
func TestWrite_InOrder(t *testing.T) {
	tmpDir := t.TempDir()
	localStorage, _ := NewStorageClient(tmpDir, 3, 1024)

	const n = 10
	filename := "concurrent_order"

	for i := range n {
		err := localStorage.Write(&types.Document{
			TracerName: filename,
			TracerID:   "cid",
			TracerData: fmt.Sprintf("data-%d", i),
		})
		if err != nil {
			t.Fatalf("write failed at %d: %v", i, err)
		}
	}

	file, err := os.Open(filepath.Join(tmpDir, filename))
	if err != nil {
		t.Fatalf("open file failed: %v", err)
	}
	defer file.Close()

	dec := json.NewDecoder(file)

	var docs []types.Document
	for dec.More() {
		var doc types.Document
		if err := dec.Decode(&doc); err != nil {
			t.Fatalf("json decode failed: %v", err)
		}
		docs = append(docs, doc)
	}

	if len(docs) != n {
		t.Fatalf("expected %d documents, got %d", n, len(docs))
	}

	for i := range n {
		want := fmt.Sprintf("data-%d", i)
		if docs[i].TracerData != want {
			t.Fatalf(
				"order mismatch at index %d: want %q, got %q",
				i, want, docs[i].TracerData,
			)
		}
	}
}

// TestWrite_EdgeCases tests edge inputs.
func TestWrite_EdgeCases(t *testing.T) {
	tmpDir := t.TempDir()
	storage, _ := NewStorageClient(tmpDir, 3, 1024)

	tests := []struct {
		name        string
		doc         *types.Document
		expectPanic bool
		wantErr     bool
	}{
		{
			name:        "nil document",
			doc:         nil,
			expectPanic: true,
		},
		{
			name: "empty tracer name",
			doc: &types.Document{
				TracerName: "",
				TracerID:   "id",
				TracerData: "data",
			},
			wantErr: false,
		},
		{
			name: "long tracer name",
			doc: &types.Document{
				TracerName: strings.Repeat("a", 100),
				TracerID:   "id",
				TracerData: "data",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.expectPanic {
				defer func() {
					if r := recover(); r == nil {
						t.Fatalf("expected panic, got none")
					}
				}()
			}

			err := storage.Write(tt.doc)

			if tt.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tt.wantErr && err != nil && !tt.expectPanic {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// FuzzWrite fuzzes Write with random inputs.
func FuzzWrite(f *testing.F) {
	f.Add("trace", "id", "data")
	f.Add("", "", "")
	f.Add(strings.Repeat("a", 50), strings.Repeat("b", 50), strings.Repeat("c", 200))

	f.Fuzz(func(t *testing.T, tracerName, tracerID, tracerData string) {
		tmpDir := t.TempDir()
		localStorage, _ := NewStorageClient(tmpDir, 3, 1024)

		doc := &types.Document{
			TracerName: tracerName,
			TracerID:   tracerID,
			TracerData: tracerData,
		}

		if err := localStorage.Write(doc); err != nil {
			t.Skipf("write failed: %v", err)
		}

		filePath := filepath.Join(tmpDir, tracerName)
		data, err := os.ReadFile(filePath)
		if err != nil {
			t.Fatalf("read failed: %v", err)
		}

		if len(data) == 0 {
			t.Fatalf("empty output")
		}
	})
}
