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

package fileutil

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// mustWriteTestFile creates a data file in the given directory using minimal
// permissions and returns its full path. It uses t.Fatalf on any setup failure.
func mustWriteTestFile(t *testing.T, dir, name string) string {
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("test content for StatInode inode verification"), 0o600); err != nil {
		t.Fatalf("os.WriteFile(%q): %v", path, err)
	}
	return path
}

// TestStatInode covers the primary paths of StatInode.
func TestStatInode(t *testing.T) {
	tmpDir := t.TempDir()

	validPath := mustWriteTestFile(t, tmpDir, "valid-inode-20250226")
	nonExistPath := filepath.Join(tmpDir, "nonexistent-file-20250226")

	tests := []struct {
		name     string
		input    string
		validate func(t *testing.T, got uint64, err error)
	}{
		{
			name:  "valid-existing-file",
			input: validPath,
			validate: func(t *testing.T, got uint64, err error) {
				if err != nil {
					t.Fatalf("StatInode: got error %v, want nil", err)
				}
				if got == 0 {
					t.Fatalf("StatInode: got 0, want non-zero inode")
				}
				// verify the returned inode matches the actual inode from the filesystem
				var s syscall.Stat_t
				if err := syscall.Stat(validPath, &s); err != nil {
					t.Fatalf("syscall.Stat verification failed: %v", err)
				}
				if got != s.Ino {
					t.Errorf("StatInode: got %d, want %d", got, s.Ino)
				}
			},
		},
		{
			name:  "nonexistent-file-returns-enoent",
			input: nonExistPath,
			validate: func(t *testing.T, got uint64, err error) {
				if err == nil {
					t.Fatalf("StatInode: got nil error, want non-nil")
				}
				if got != 0 {
					t.Errorf("StatInode: got inode %d, want 0 on error", got)
				}
				if !errors.Is(err, syscall.ENOENT) {
					t.Errorf("got error %v, want syscall.ENOENT", err)
				}
			},
		},
		{
			name:  "empty-path-returns-error",
			input: "",
			validate: func(t *testing.T, got uint64, err error) {
				if err == nil {
					t.Fatalf("StatInode: got nil error for empty path, want non-nil")
				}
				if got != 0 {
					t.Errorf("StatInode: got inode %d, want 0", got)
				}
				if !errors.Is(err, syscall.ENOENT) {
					t.Errorf("got error %v, want syscall.ENOENT", err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := StatInode(tt.input)
			tt.validate(t, got, err)
		})
	}
}
