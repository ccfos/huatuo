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

package blockdevice

import (
	"os"
	"path/filepath"
	"testing"

	"huatuo-bamai/internal/procfs"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var rightFSCheck = func(t *testing.T, fs FS, err error) {
	assert.NoError(t, err)
	assert.NotEqual(t, FS{}, fs)
}

var errorFSCheck = func(t *testing.T, fs FS, err error) {
	assert.Error(t, err)
	assert.Equal(t, FS{}, fs)
}

// setupMounts creates the specified mount point directories under tmpDir
func setupMounts(t *testing.T, tmpDir string, mounts []string) {
	for _, mount := range mounts {
		path := filepath.Join(tmpDir, mount)
		require.NoError(t, os.MkdirAll(path, 0o755))
	}
}

// setupFile creates a file with content at the specified mount path under tmpDir
func setupFile(t *testing.T, tmpDir, mount string, content []byte) {
	path := filepath.Join(tmpDir, mount)
	require.NoError(t, os.WriteFile(path, content, 0o600))
}

func TestNewDefaultFS_Filesystem(t *testing.T) {
	originalPrefix := filepath.Dir(procfs.DefaultPath())
	defer procfs.RootPrefix(originalPrefix)

	tests := []struct {
		name     string
		setup    func(*testing.T) string
		validate func(*testing.T, FS, error)
	}{
		{
			name: "valid proc and sys directories",
			setup: func(t *testing.T) string {
				tmpDir := t.TempDir()
				setupMounts(t, tmpDir, []string{"proc", "sys"})
				return tmpDir
			},
			validate: rightFSCheck,
		},
		{
			name: "both directories do not exist",
			setup: func(t *testing.T) string {
				return t.TempDir()
			},
			validate: errorFSCheck,
		},
		{
			name: "proc is file instead of directory",
			setup: func(t *testing.T) string {
				tmpDir := t.TempDir()
				setupFile(t, tmpDir, "proc", []byte("not a directory"))
				return tmpDir
			},
			validate: errorFSCheck,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpRoot := tt.setup(t)
			procfs.RootPrefix(tmpRoot)

			fs, err := NewDefaultFS()
			tt.validate(t, fs, err)
		})
	}
}

// Integration Tests (Real Environment)
// TEST_INTEGRATION=true go test -v ./internal/procfs/blockdevice/...
func TestNewDefaultFS_Integration(t *testing.T) {
	if os.Getenv("TEST_INTEGRATION") == "" {
		t.Skip("Set TEST_INTEGRATION=true to run integration tests")
	}

	fs, err := NewDefaultFS()
	rightFSCheck(t, fs, err)
}
