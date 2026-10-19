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
	"os"
	"path/filepath"
	"testing"
)

func TestStatInodeSuccess(t *testing.T) {
	f, err := os.CreateTemp("", "huatuo-test-*")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	defer os.Remove(f.Name())

	ino, err := StatInode(f.Name())
	if err != nil {
		t.Fatalf("StatInode(%q) error = %v", f.Name(), err)
	}
	if ino == 0 {
		t.Errorf("StatInode(%q) = 0, want non-zero", f.Name())
	}
}

func TestStatInodeMissingFile(t *testing.T) {
	_, err := StatInode("/does/not/exist/huatuo-test-missing")
	if err == nil {
		t.Fatal("StatInode() error = nil, want non-nil")
	}
	if !os.IsNotExist(err) {
		t.Errorf("StatInode() error = %v, want os.IsNotExist error", err)
	}
}

func TestRemoveIfSame(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T, path string) os.FileInfo
		wantExists bool
	}{
		{
			name: "removes the expected file",
			setup: func(t *testing.T, path string) os.FileInfo {
				t.Helper()
				writeFile(t, path, "first")
				return statFile(t, path)
			},
		},
		{
			name: "keeps a replaced file",
			setup: func(t *testing.T, path string) os.FileInfo {
				t.Helper()
				writeFile(t, path, "first")
				expected := statFile(t, path)
				replacement := path + ".replacement"
				writeFile(t, replacement, "second")
				if err := os.Rename(replacement, path); err != nil {
					t.Fatalf("replace file: %v", err)
				}
				return expected
			},
			wantExists: true,
		},
		{
			name: "ignores a missing path",
			setup: func(t *testing.T, path string) os.FileInfo {
				t.Helper()
				writeFile(t, path, "first")
				expected := statFile(t, path)
				if err := os.Remove(path); err != nil {
					t.Fatalf("remove first file: %v", err)
				}
				return expected
			},
		},
		{
			name: "ignores a nil expectation",
			setup: func(t *testing.T, path string) os.FileInfo {
				t.Helper()
				writeFile(t, path, "first")
				return nil
			},
			wantExists: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "owned")
			expected := tt.setup(t, path)

			if err := RemoveIfSame(path, expected); err != nil {
				t.Fatalf("RemoveIfSame() error = %v", err)
			}
			_, err := os.Lstat(path)
			if got := err == nil; got != tt.wantExists {
				t.Fatalf("RemoveIfSame() exists = %t, want %t (err=%v)", got, tt.wantExists, err)
			}
		})
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func statFile(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info
}
