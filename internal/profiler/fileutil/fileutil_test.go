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

func TestCopyDirCopiesNestedFiles(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "copied")
	if err := os.MkdirAll(filepath.Join(src, "nested"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "root.txt"), []byte("root"), 0o600); err != nil {
		t.Fatalf("WriteFile(root) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "nested", "child.txt"), []byte("child"), 0o600); err != nil {
		t.Fatalf("WriteFile(child) error = %v", err)
	}

	if err := CopyDir(src, dst); err != nil {
		t.Fatalf("CopyDir() error = %v", err)
	}
	for path, want := range map[string]string{
		filepath.Join(dst, "root.txt"):            "root",
		filepath.Join(dst, "nested", "child.txt"): "child",
	} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%q) error = %v", path, err)
		}
		if string(got) != want {
			t.Errorf("ReadFile(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestCopyFileOverwritesDestinationWithStandardMode(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.txt")
	dst := filepath.Join(dir, "destination.txt")
	if err := os.WriteFile(src, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFile(source) error = %v", err)
	}
	if err := os.WriteFile(dst, []byte("old"), 0o600); err != nil {
		t.Fatalf("WriteFile(destination) error = %v", err)
	}

	if err := CopyFile(src, dst); err != nil {
		t.Fatalf("CopyFile() error = %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != "new" {
		t.Errorf("destination content = %q, want %q", got, "new")
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o644 {
		t.Errorf("destination mode = %o, want 644", gotMode)
	}
}
