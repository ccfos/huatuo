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
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestCopyFilePreservesSourceMode(t *testing.T) {
	t.Parallel()

	content := []byte("profiler-runtime-file")

	tests := []struct {
		name      string
		srcMode   os.FileMode
		dstExists bool
		dstMode   os.FileMode
		dstBody   []byte
	}{
		{
			name:    "executable source",
			srcMode: 0o755,
		},
		{
			name:    "non-executable source",
			srcMode: 0o644,
		},
		{
			name:      "overwrite destination with different mode",
			srcMode:   0o755,
			dstExists: true,
			dstMode:   0o600,
			dstBody:   []byte("stale-destination"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			src := filepath.Join(dir, "src")
			dst := filepath.Join(dir, "dst")
			writeFileWithMode(t, src, content, tt.srcMode)
			if tt.dstExists {
				writeFileWithMode(t, dst, tt.dstBody, tt.dstMode)
			}

			if err := CopyFile(src, dst); err != nil {
				t.Fatalf("CopyFile() error = %v", err)
			}

			got, err := os.ReadFile(dst)
			if err != nil {
				t.Fatalf("ReadFile(%q) error = %v", dst, err)
			}
			if !bytes.Equal(got, content) {
				t.Fatalf("destination content = %q, want %q", got, content)
			}
			assertMode(t, dst, tt.srcMode)
		})
	}
}

func TestCopyFileErrors(t *testing.T) {
	t.Parallel()

	t.Run("missing source", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		err := CopyFile(filepath.Join(dir, "missing"), filepath.Join(dir, "dst"))
		if err == nil {
			t.Fatal("CopyFile() error = nil, want non-nil")
		}
	})

	t.Run("missing destination parent", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		src := filepath.Join(dir, "src")
		writeFileWithMode(t, src, []byte("payload"), 0o644)
		err := CopyFile(src, filepath.Join(dir, "missing-parent", "dst"))
		if err == nil {
			t.Fatal("CopyFile() error = nil, want non-nil")
		}
	})
}

func TestCopyDirPreservesFilePermissions(t *testing.T) {
	t.Parallel()

	srcDir := t.TempDir()
	dstDir := t.TempDir()
	binDir := filepath.Join(srcDir, "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatalf("Mkdir(%q) error = %v", binDir, err)
	}

	writeFileWithMode(t, filepath.Join(binDir, "tool"), []byte("#!/bin/sh\n"), 0o755)
	writeFileWithMode(t, filepath.Join(srcDir, "readme"), []byte("docs"), 0o644)

	if err := CopyDir(srcDir, dstDir); err != nil {
		t.Fatalf("CopyDir() error = %v", err)
	}

	assertMode(t, filepath.Join(dstDir, "bin", "tool"), 0o755)
	assertMode(t, filepath.Join(dstDir, "readme"), 0o644)

	got, err := os.ReadFile(filepath.Join(dstDir, "bin", "tool"))
	if err != nil {
		t.Fatalf("ReadFile(tool) error = %v", err)
	}
	if !bytes.Equal(got, []byte("#!/bin/sh\n")) {
		t.Fatalf("copied tool content = %q, want shebang payload", got)
	}
}

func writeFileWithMode(t *testing.T, path string, body []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, body, mode); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("Chmod(%q, %04o) error = %v", path, mode, err)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", path, err)
	}
	if got := info.Mode().Perm(); got != want.Perm() {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want.Perm())
	}
}
