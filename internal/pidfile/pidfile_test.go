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

package pidfile

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func redirectPidDir(t *testing.T) {
	t.Helper()
	old := defaultDirPath
	defaultDirPath = t.TempDir()
	t.Cleanup(func() { defaultDirPath = old })
}

func TestPath(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"app", "/var/run/app.pid"},
		{"nginx", "/var/run/nginx.pid"},
		{"", "/var/run/.pid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := path(tt.name); got != tt.want {
				t.Errorf("path(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestLock_Success(t *testing.T) {
	redirectPidDir(t)

	name := "testapp"
	lk, err := Lock(name)
	require.NoError(t, err)
	t.Cleanup(lk.Unlock)

	data, err := os.ReadFile(path(name))
	require.NoError(t, err)
	pid, err := strconv.Atoi(string(data))
	require.NoError(t, err)
	if pid != os.Getpid() {
		t.Errorf("pidfile pid = %d, want %d", pid, os.Getpid())
	}
}

func TestLock_AlreadyLocked(t *testing.T) {
	redirectPidDir(t)

	name := "test-locked"
	lk, err := Lock(name)
	require.NoError(t, err)
	t.Cleanup(lk.Unlock)

	_, err = Lock(name)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already running")
}

func TestUnlock_RemovesFile(t *testing.T) {
	redirectPidDir(t)

	name := "toremove"
	lk, err := Lock(name)
	require.NoError(t, err)

	lk.Unlock()

	if _, err := os.Stat(path(name)); !os.IsNotExist(err) {
		t.Errorf("Stat after Unlock: err = %v, want IsNotExist", err)
	}
}

func TestUnlock_Idempotent(t *testing.T) {
	redirectPidDir(t)

	lk, err := Lock("twice")
	require.NoError(t, err)

	lk.Unlock()
	lk.Unlock()
}

// TestLock_HandleKeepsFlockAlive guards the bug that motivated the
// Handle redesign: in the previous API, Lock opened *os.File in a local
// variable that became unreachable on return. After GC ran the finalizer
// closed the fd, releasing the flock while the pid file stayed on disk —
// a second Lock would then succeed. The new contract is that holding the
// returned *Handle keeps the fd reachable, so GC cannot release the lock.
func TestLock_HandleKeepsFlockAlive(t *testing.T) {
	redirectPidDir(t)

	name := "gcsurvive"
	lk, err := Lock(name)
	require.NoError(t, err)
	t.Cleanup(lk.Unlock)

	// Force two GC cycles so any unreachable *os.File would have its
	// finalizer run and its fd closed before the second Lock attempt.
	runtime.GC()
	runtime.GC()

	if _, err := Lock(name); err == nil {
		t.Fatalf("Lock(%q) after GC = nil, want error (handle should still hold flock)", name)
	}
}

func TestRead(t *testing.T) {
	tmp := t.TempDir()
	pidPath := filepath.Join(tmp, "test.pid")

	tests := []struct {
		name    string
		content string
		want    int
		wantErr bool
	}{
		{"normal", "12345", 12345, false},
		{"with space", "  67890  \n", 67890, false},
		{"negative", "-1", -1, false},
		{"empty", "", 0, true},
		{"invalid", "abc", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, os.WriteFile(pidPath, []byte(tt.content), 0o600))

			got, err := Read(pidPath)
			if tt.wantErr {
				if err == nil {
					t.Errorf("Read(%q) err = nil, want error", tt.content)
				}
				if got != 0 {
					t.Errorf("Read(%q) = %d, want 0", tt.content, got)
				}

				return
			}
			require.NoError(t, err)
			if got != tt.want {
				t.Errorf("Read(%q) = %d, want %d", tt.content, got, tt.want)
			}
		})
	}
}

func TestRead_NotExist(t *testing.T) {
	_, err := Read("/this/file/does/not/exist.pid")
	require.Error(t, err)
	assert.True(t, os.IsNotExist(err))
}
