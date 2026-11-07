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
	"errors"
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

	rotator, err := NewFileRotator(path, maxRotation, rotationSize)
	if err != nil {
		t.Fatalf("NewSizeRotator returned error: %v", err)
	}
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

	rotator, err := NewFileRotator(path, maxRotation, rotationSize)
	if err != nil {
		t.Fatalf("NewFileRotator returned error: %v", err)
	}

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

	r, err := NewFileRotator(path, maxBackups, rotationSize)
	if err != nil {
		t.Fatalf("NewFileRotator returned error: %v", err)
	}

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
	rotator, err := NewFileRotator(path, 3, 1)
	if err != nil {
		t.Fatalf("NewFileRotator returned error: %v", err)
	}

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
	r, err := NewFileRotator(tmpFile, 3, 1)
	if err != nil {
		t.Fatalf("NewFileRotator returned error: %v", err)
	}

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
	r, err := NewFileRotator("", 3, 1)
	if err != nil {
		t.Fatalf("NewFileRotator returned error: %v", err)
	}
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

// TestFileRotator_RotationSizeValidation locks in construction-time
// validation of rotationSize: non-positive sizes are rejected explicitly
// instead of silently falling back to lumberjack's 100 MB default (zero) or
// having every write refused by lumberjack (negative), and a positive size
// keeps working unchanged.
func TestFileRotator_RotationSizeValidation(t *testing.T) {
	tests := []struct {
		name         string
		rotationSize int
		wantErr      bool
	}{
		{name: "zero silently falls back to default", rotationSize: 0, wantErr: true},
		{name: "negative refuses every write", rotationSize: -1, wantErr: true},
		{name: "positive is accepted", rotationSize: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rotator, err := NewFileRotator(filepath.Join(t.TempDir(), "app.log"), 3, tt.rotationSize)
			if tt.wantErr {
				if err == nil {
					rotator.Close()
					t.Fatalf("NewFileRotator(rotationSize=%d) succeeded, want error", tt.rotationSize)
				}
				if !errors.Is(err, ErrInvalidRotationSize) {
					t.Errorf("NewFileRotator(rotationSize=%d) error = %v, want ErrInvalidRotationSize", tt.rotationSize, err)
				}
				if rotator != nil {
					t.Errorf("NewFileRotator(rotationSize=%d) returned both a rotator and an error", tt.rotationSize)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewFileRotator(rotationSize=%d) returned error: %v", tt.rotationSize, err)
			}
			if rotator == nil {
				t.Fatal("NewFileRotator returned nil")
			}
			if err := rotator.Close(); err != nil {
				t.Errorf("Close() failed: %v", err)
			}
		})
	}
}

// TestFileRotator_PositiveRotationSizeUnchanged verifies a positive rotation
// size still writes payload to the log file without rotating.
func TestFileRotator_PositiveRotationSizeUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	rotator, err := NewFileRotator(path, 3, 1)
	if err != nil {
		t.Fatalf("NewFileRotator returned error: %v", err)
	}
	defer func() {
		if err := rotator.Close(); err != nil {
			t.Errorf("Close() failed: %v", err)
		}
	}()

	payload := []byte("payload")
	n, err := rotator.Write(payload)
	if err != nil {
		t.Fatalf("Write() failed: %v", err)
	}
	if n != len(payload) {
		t.Errorf("Write() = %d bytes, want %d", n, len(payload))
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() failed: %v", err)
	}
	if !bytes.Equal(content, payload) {
		t.Errorf("file content = %q, want %q", content, payload)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() failed: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("a small write must not rotate the log file, got %d files", len(entries))
	}
}

// TestFileRotator_InvalidPath tests behavior when the log file path is invalid.
// Writing to an invalid or non-existent directory should return an error.
func TestFileRotator_FailsOnInvalidPath(t *testing.T) {
	invalidDir := filepath.Join(string([]byte{0x00}), "invalid") // null byte to make invalid path
	r, err := NewFileRotator(filepath.Join(invalidDir, "log"), 3, 1)
	if err != nil {
		t.Fatalf("NewFileRotator returned error: %v", err)
	}
	defer func() {
		// Close may fail due to invalid path, ignore
		_ = r.Close()
	}()

	_, err = r.Write([]byte("test"))
	if err == nil {
		t.Error("expected error with invalid path, but got none")
	}
}
