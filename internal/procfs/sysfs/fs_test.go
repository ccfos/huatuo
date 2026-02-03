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
package sysfs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"huatuo-bamai/internal/procfs"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultPath(t *testing.T) {
	got := DefaultPath()
	assert.Equal(t, "/sys", got)
}

func TestNewDefaultFS_Filesystem(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(t *testing.T) string
		wantErr     bool
		errContains string
	}{
		{
			name: "valid path directory",
			setup: func(t *testing.T) string {
				tmpDir := t.TempDir()
				sysPath := filepath.Join(tmpDir, "sys")
				require.NoError(t, os.MkdirAll(sysPath, 0o755))
				return tmpDir
			},
			wantErr: false,
		},
		{
			name: "invalid file path",
			setup: func(t *testing.T) string {
				tmpDir := t.TempDir()
				sysPath := filepath.Join(tmpDir, "file")
				require.NoError(t, os.WriteFile(sysPath, []byte("invalid file"), 0o600))
				return sysPath
			},
			wantErr: true,
		},
		{
			name: "non-existent path",
			setup: func(t *testing.T) string {
				return "/nonexistent/path/xyz"
			},
			wantErr: true,
		},
		{
			name: "valid --sys!! path",
			setup: func(t *testing.T) string {
				tmpDir := t.TempDir()
				sysPath := filepath.Join(tmpDir, "--sys!!")
				require.NoError(t, os.MkdirAll(filepath.Join(sysPath, "sys"), 0o755))
				return sysPath
			},
			wantErr: false,
		},
	}
	originalPrefix := strings.TrimSuffix(DefaultPath(), "sys")
	defer func() { procfs.RootPrefix(originalPrefix) }()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			procfs.RootPrefix(tt.setup(t))

			fs, err := NewDefaultFS()
			if tt.wantErr {
				assert.Error(t, err)
				assert.Equal(t, FS{}, fs)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, fs)
			}
		})
	}
}

func TestPath(t *testing.T) {
	tests := []struct {
		name  string
		paths []string
		want  string
	}{
		{
			name:  "special single component",
			paths: []string{"//dev"},
			want:  "/sys/dev",
		},
		{
			name:  "deep path",
			paths: []string{"class", "net", "eth0"},
			want:  "/sys/class/net/eth0",
		},
		{
			name:  "empty paths",
			paths: []string{},
			want:  "/sys",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Path(tt.paths...)
			assert.Equal(t, tt.want, got)
		})
	}
}

// Integration Tests (Real Environment)
// TEST_INTEGRATION=true go test -v ./internal/procfs/sysfs/...
func TestNewDefaultFS_Integration(t *testing.T) {
	if os.Getenv("TEST_INTEGRATION") == "" {
		t.Skip("Set TEST_INTEGRATION=true to run integration tests")
	}

	fs, err := NewDefaultFS()
	if err != nil {
		t.Skipf("/sys not available: %v", err)
	}
	assert.NotNil(t, fs)
}
