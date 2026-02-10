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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var rightProcCheck = func(t *testing.T, proc Proc, err error) {
	assert.NoError(t, err)
	assert.NotEqual(t, Proc{}, proc)
}

var errorProcCheck = func(t *testing.T, proc Proc, err error) {
	assert.Error(t, err)
	assert.Equal(t, Proc{}, proc)
}

func TestNewProc_Filesystem(t *testing.T) {
	tmpRoot := t.TempDir()
	originalPrefix := filepath.Dir(DefaultPath())
	defer RootPrefix(originalPrefix)

	tests := []struct {
		name     string
		pid      int
		setup    func(*testing.T) string
		validate func(*testing.T, Proc, error)
	}{
		{
			name: "valid process",
			pid:  1,
			setup: func(t *testing.T) string {
				procPath := filepath.Join(tmpRoot, "proc", "1")
				require.NoError(t, os.MkdirAll(procPath, 0o755))
				return tmpRoot
			},
			validate: rightProcCheck,
		},
		{
			name: "non-existent process",
			pid:  1,
			setup: func(t *testing.T) string {
				procPath := filepath.Join(tmpRoot, "non-existent")
				return procPath
			},
			validate: errorProcCheck,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefix := tt.setup(t)
			RootPrefix(prefix)
			proc, err := NewProc(tt.pid)
			tt.validate(t, proc, err)
		})
	}
}

func TestSelf_Filesystem(t *testing.T) {
	tmpRoot := t.TempDir()
	originalPrefix := filepath.Dir(DefaultPath())
	RootPrefix(tmpRoot)
	defer RootPrefix(originalPrefix)

	// case 1: empty self symlink
	procPath := filepath.Join(tmpRoot, "proc")
	pid1Path := filepath.Join(procPath, "1")
	require.NoError(t, os.MkdirAll(pid1Path, 0o755))
	proc, err := Self()
	errorProcCheck(t, proc, err)

	// case 2: self symlink to PID 1

	selfPath := filepath.Join(procPath, "self")
	require.NoError(t, os.Symlink("1", selfPath))

	proc, err = Self()
	rightProcCheck(t, proc, err)
	assert.Equal(t, 1, proc.PID, "Self() should return PID 1")
}

// Integration Tests (Real Environment)
// TEST_INTEGRATION=true go test -v ./internal/procfs/...
func TestNewProc_Integration(t *testing.T) {
	if os.Getenv("TEST_INTEGRATION") == "" {
		t.Skip("Set TEST_INTEGRATION=true to run integration tests")
	}

	proc, err := NewProc(1)
	if err != nil {
		t.Skipf("PID 1 not available: %v", err)
	}
	rightProcCheck(t, proc, err)

	proc, err = NewProc(999999999)
	errorProcCheck(t, proc, err)
}

func TestSelf_Integration(t *testing.T) {
	if os.Getenv("TEST_INTEGRATION") == "" {
		t.Skip("Set TEST_INTEGRATION=true to run integration tests")
	}

	proc, err := Self()
	if err != nil {
		t.Skipf("self process not available: %v", err)
	}
	rightProcCheck(t, proc, err)
}
