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

package procfs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rightCheck(t *testing.T, fs FS, err error) {
	assert.NoError(t, err)
	assert.NotNil(t, fs)
}

func TestRootPrefixUpdatesMountPoints(t *testing.T) {
	tmpRoot := t.TempDir()
	originalPrefix := strings.TrimSuffix(DefaultPath(), "proc")
	RootPrefix(tmpRoot)
	defer func() { RootPrefix(originalPrefix) }()
	expectedProc := filepath.Join(tmpRoot, "/proc")
	expectedSys := filepath.Join(tmpRoot, "/sys")
	expectedDev := filepath.Join(tmpRoot, "/dev")

	assert.Equal(t, expectedProc, defaultProcMountPoint)
	assert.Equal(t, expectedSys, defaultSysMountPoint)
	assert.Equal(t, expectedDev, defaultDevMountPoint)
}

func TestNewDefaultFS_Filesystem(t *testing.T) {
	tmpRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpRoot, "proc"), 0o755))
	originalPrefix := strings.TrimSuffix(DefaultPath(), "proc")
	RootPrefix(tmpRoot)
	defer func() { RootPrefix(originalPrefix) }()
	fs, err := NewDefaultFS()
	rightCheck(t, fs, err)
}

func TestNewFS(t *testing.T) {
	tmpRoot := t.TempDir()
	tests := []struct {
		name     string
		setup    func(*testing.T) string
		validate func(*testing.T, FS, error)
	}{
		{
			name: "valid proc directory",
			setup: func(t *testing.T) string {
				procPath := filepath.Join(tmpRoot, "proc")
				require.NoError(t, os.MkdirAll(procPath, 0o755))
				return procPath
			},
			validate: rightCheck,
		},
		{
			name: "invalid file path",
			setup: func(t *testing.T) string {
				procPath := filepath.Join(tmpRoot, "file")
				require.NoError(t, os.WriteFile(procPath, []byte(""), 0o600))
				return procPath
			},
			validate: func(t *testing.T, fs FS, err error) { assert.Error(t, err) },
		},
		{
			name: "non-existent path",
			setup: func(t *testing.T) string {
				return "/nonexistent/path/"
			},
			validate: func(t *testing.T, fs FS, err error) { assert.Error(t, err) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			procPath := tt.setup(t)
			fs, err := NewFS(procPath)
			tt.validate(t, fs, err)
		})
	}
}

func TestPath(t *testing.T) {
	tempRoot := t.TempDir()
	originalPrefix := strings.TrimSuffix(DefaultPath(), "proc")
	RootPrefix(tempRoot)
	defer func() { RootPrefix(originalPrefix) }()

	expectedBase := filepath.Join(tempRoot, "/proc")
	assert.Equal(t, expectedBase, DefaultPath())

	expectedPath := filepath.Join(expectedBase, "dira", "dirb")
	assert.Equal(t, Path("dira", "dirb"), expectedPath)
}

func TestDefaultPathByType(t *testing.T) {
	defaultMounts := []string{"/proc", "/sys", "/dev"}
	for _, mount := range defaultMounts {
		typ := strings.TrimPrefix(mount, "/")
		assert.Equal(t, mount, DefaultPathByType(typ))
	}
	assert.Equal(t, "", DefaultPathByType("unknown"))
}

// Integration Tests (Real Environment)
// TEST_INTEGRATION=true go test -v ./internal/procfs/...
func TestNewDefaultFS_Integration(t *testing.T) {
	if os.Getenv("TEST_INTEGRATION") == "" {
		t.Skip("Set TEST_INTEGRATION=true to run integration tests")
	}

	fs, err := NewDefaultFS()
	rightCheck(t, fs, err)
}
