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

package rotator

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewSizeRotator(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.log")
	maxRotation := 3
	rotationSize := 1 // 1 MB

	rotator := NewSizeRotator(path, maxRotation, rotationSize)
	if rotator == nil {
		t.Fatal("NewSizeRotator returned nil")
	}

	// Clean up
	if err := rotator.Close(); err != nil {
		t.Errorf("failed to close rotator: %v", err)
	}
}

func TestFileRotator_WriteAndRotate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	maxRotation := 2
	rotationSize := 1 // 1 MB

	rotator := NewSizeRotator(path, maxRotation, rotationSize)

	// Write 0.5 MB data less than rotation size
	data := make([]byte, 512*1024)
	if _, err := rotator.Write(data); err != nil {
		t.Errorf("write failed: %v", err)
	}

	// Check file exists and content
	content, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("failed to read file: %v", err)
	}
	if len(content) != len(data) {
		t.Errorf("expected file size %d, got %d", len(data), len(content))
	}

	// Write another 0.5 MB
	if _, err = rotator.Write(data); err != nil {
		t.Errorf("second write failed: %v", err)
	}

	// Write a bit more to ensure rotation
	if _, err = rotator.Write([]byte{'b'}); err != nil {
		t.Errorf("extra write failed: %v", err)
	}

	// Close to flush
	if err := rotator.Close(); err != nil {
		t.Errorf("close failed: %v", err)
	}

	// Check backup files
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Errorf("failed to read dir: %v", err)
	}

	// test.log and test-xxx.log
	backups := 0
	for _, f := range files {
		if f.Name() != "test.log" {
			backups++
		}
	}
	if backups < 1 {
		t.Errorf("expected at least one backup, got %d", backups)
	}
}

func TestFileRotator_MaxBackups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	maxRotation := 2
	rotationSize := 1 // 1 MB

	rotator := NewSizeRotator(path, maxRotation, rotationSize)

	data := make([]byte, 1024*1024)

	for i := 0; i < 4; i++ { // Enough to create more than max backups
		if _, err := rotator.Write(data); err != nil {
			t.Errorf("write %d failed: %v", i, err)
		}
		time.Sleep(1 * time.Millisecond) // Ensure different timestamps
	}

	if err := rotator.Close(); err != nil {
		t.Errorf("close failed: %v", err)
	}

	// Check number of backups
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Errorf("failed to read dir: %v", err)
	}

	backups := 0
	for _, f := range files {
		if f.Name() != "test.log" {
			backups++
		}
	}
	if backups > maxRotation {
		t.Errorf("expected at most %d backups, got %d", maxRotation, backups)
	}
}

func TestFileRotator_CloseAndWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.log")
	rotator := NewSizeRotator(path, 3, 1)

	if err := rotator.Close(); err != nil {
		t.Errorf("close failed: %v", err)
	}

	// Although file is closed, you can still write.
	if _, err := rotator.Write([]byte("after close")); err != nil {
		t.Errorf("expected nil when writing after close, got %v", err)
	}
}
