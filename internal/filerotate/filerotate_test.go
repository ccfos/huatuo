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

package filerotate

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewSizeRotator(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.log")
	maxRotation := 3
	rotationSize := 1 // 1 MB

	rotator := NewFileRotator(path, maxRotation, rotationSize)
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

	rotator := NewFileRotator(path, maxRotation, rotationSize)

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

// TestFileRotator_RotationAndMaxBackups verifies that:
// 1. log rotation happens
// 2. the current log file exists
// 3. the number of backup files never exceeds maxBackups
func TestFileRotator_RotationAndMaxBackups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	maxBackups := 2
	rotationSize := 1 // 1 MB

	r := NewFileRotator(path, maxBackups, rotationSize)

	// Write enough data to trigger multiple rotations
	data := bytes.Repeat([]byte("a"), 1024*1024/10) // 0.1MB per write
	for i := range 40 {
		if _, err := r.Write(data); err != nil {
			t.Fatalf("write %d failed: %v", i, err)
		}
		time.Sleep(1 * time.Millisecond) // Ensure different timestamps
	}

	if err := r.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}

	// Check number of backups
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}

	var (
		currentExists bool
		backups       []string
	)

	for _, entry := range entries {
		name := entry.Name()

		switch {
		case name == "test.log":
			currentExists = true
		case strings.HasPrefix(name, "test-") && strings.HasSuffix(name, ".log"):
			backups = append(backups, name)
		}
	}

	if !currentExists {
		t.Errorf("current log file test.log does not exist")
	}

	// do not exceeds maxBackups
	if len(backups) > maxBackups {
		t.Errorf(
			"expected at most %d backup files, got %d: %v",
			maxBackups,
			len(backups),
			backups,
		)
	}
}

func TestFileRotator_CloseAndWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.log")
	rotator := NewFileRotator(path, 3, 1)

	if err := rotator.Close(); err != nil {
		t.Errorf("close failed: %v", err)
	}

	// Although file is closed, you can still write.
	if _, err := rotator.Write([]byte("after close")); err != nil {
		t.Errorf("expected nil when writing after close, got %v", err)
	}
}

// TestFileRotator_MultipleWrites tests multiple write operations with different data.
func TestFileRotator_MultipleWrites(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "multi.log")
	r := NewFileRotator(tmpFile, 3, 1)

	defer func() {
		if err := r.Close(); err != nil {
			t.Errorf("Close() failed: %v", err)
		}
	}()

	var lines [][]byte
	for i := range 10 {
		// Initialize line data with index suffix
		line := []byte(fmt.Sprintf("abc123_xyz_%d\n", i))
		lines = append(lines, line)

		if _, err := r.Write(line); err != nil {
			t.Fatalf("Write(%d) failed: %v", i, err)
		}
	}

	content, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("ReadFile() failed: %v", err)
	}

	for _, line := range lines {
		// Remove newline character to count exact occurrences
		expected := line[:len(line)-1]
		count := bytes.Count(content, expected)
		if count != 1 {
			t.Errorf("expected content %q to appear once, got %d", expected, count)
		}
	}
}

// TestFileRotator_EmptyFilename tests behavior with empty filename,
// an empty filename is allowed and should fall back to lumberjack's default behavior.
func TestFileRotator_EmptyFilenameAllowed(t *testing.T) {
	r := NewFileRotator("", 3, 1)
	defer func() {
		if err := r.Close(); err != nil {
			t.Logf("Close returned error: %v", err)
		}
	}()

	data := []byte("test data with empty filename")
	n, err := r.Write(data)
	if err != nil {
		t.Errorf("unexpected error with empty filename: %v", err)
	}
	if n != len(data) {
		t.Errorf("Write returned %d bytes, expected %d", n, len(data))
	}
}

// TestFileRotator_InvalidPath tests behavior when the log file path is invalid.
// Writing to an invalid or non-existent directory should return an error.
func TestFileRotator_FailsOnInvalidPath(t *testing.T) {
	invalidDir := filepath.Join(string([]byte{0x00}), "invalid") // null byte to make invalid path
	r := NewFileRotator(filepath.Join(invalidDir, "log"), 3, 1)
	defer func() {
		// Close may fail due to invalid path, ignore
		_ = r.Close()
	}()

	_, err := r.Write([]byte("test"))
	if err == nil {
		t.Error("expected error with invalid path, but got none")
	}
}
