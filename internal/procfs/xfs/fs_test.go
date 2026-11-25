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

package xfs

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/ccfos/huatuo/internal/procfs"

	"github.com/prometheus/procfs/xfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rightFSCheck(t *testing.T, fs FS, err error) {
	assert.NoError(t, err)
	assert.NotEqual(t, FS{}, fs)
}

func errorFSCheck(t *testing.T, fs FS, err error) {
	assert.Error(t, err)
	assert.Equal(t, FS{}, fs)
}

func setupMounts(t *testing.T, tmpDir string, mounts []string) {
	for _, mount := range mounts {
		path := filepath.Join(tmpDir, mount)
		require.NoError(t, os.MkdirAll(path, 0o755))
	}
}

func setupFile(t *testing.T, tmpDir, mount string, content []byte) {
	path := filepath.Join(tmpDir, mount)
	require.NoError(t, os.WriteFile(path, content, 0o600))
}

func TestNewDefaultFS(t *testing.T) {
	originalPrefix := filepath.Dir(procfs.DefaultPath())
	defer procfs.RootPrefix(originalPrefix)

	tests := []struct {
		name     string
		setup    func(*testing.T) string
		validate func(*testing.T, FS, error)
	}{
		{
			name: "valid proc and sys",
			setup: func(t *testing.T) string {
				tmpDir := t.TempDir()
				setupMounts(t, tmpDir, []string{"proc", "sys"})
				return tmpDir
			},
			validate: rightFSCheck,
		},
		{
			name: "missing proc and sys",
			setup: func(t *testing.T) string {
				return t.TempDir()
			},
			validate: errorFSCheck,
		},
		{
			name: "proc is file",
			setup: func(t *testing.T) string {
				tmpDir := t.TempDir()
				setupFile(t, tmpDir, "proc", []byte("not dir"))
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

func TestDefaultPath(t *testing.T) {
	tmpRoot := t.TempDir()
	originalPrefix := strings.TrimSuffix(DefaultPath(), "sys/fs/xfs")
	defer func() { procfs.RootPrefix(originalPrefix) }()

	wantedPath := filepath.Join(tmpRoot, "sys", "fs", "xfs")
	procfs.RootPrefix(tmpRoot)
	got := DefaultPath()
	assert.Equal(t, wantedPath, got)
}

// Integration Test (run with TEST_INTEGRATION=true)
// TEST_INTEGRATION=true go test -v ./internal/procfs/...
func TestNewDefaultFS_Integration(t *testing.T) {
	if os.Getenv("TEST_INTEGRATION") == "" {
		t.Skip("Set TEST_INTEGRATION=true to run")
	}
	fs, err := NewDefaultFS()
	rightFSCheck(t, fs, err)
}

func TestParseDeviceStats(t *testing.T) {
	const collected = "extent_alloc 1 2 3 4\nig 5 6 7 8 9 10 11\n" +
		"push_ail 12 13 14 15 16 17 18 19 20 21\nbuf 22 23 24 25 26 27 28 29 30\n"
	want, err := xfs.ParseStats(strings.NewReader(collected))
	require.NoError(t, err)

	for _, tt := range []struct {
		name string
		data string
	}{
		{
			name: "baseline",
			data: collected,
		},
		{
			name: "nested labels",
			data: collected + "gc xpc 18446744073709551615\n",
		},
		{
			name: "wide counter",
			data: collected + "defer_relog 4294967296\n",
		},
		{
			name: "unused known group",
			data: collected + "log invalid\n",
		},
		{
			name: "unknown group",
			data: collected + "future_stats nested 1\n",
		},
		{
			name: "whitespace",
			data: "\n\t" + strings.ReplaceAll(collected, " ", "\t"),
		},
		{
			name: "no trailing newline",
			data: strings.TrimSuffix(collected, "\n"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDeviceStats(strings.NewReader(tt.data))
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}

	for _, group := range []string{"extent_alloc", "ig", "push_ail", "buf"} {
		for _, value := range []string{"invalid", "4294967296", "1"} {
			t.Run(group+"/"+value, func(t *testing.T) {
				stats, err := parseDeviceStats(strings.NewReader(collected + group + " " + value + "\n"))
				assert.Error(t, err)
				assert.Nil(t, stats)
			})
		}
	}

	_, err = parseDeviceStats(iotest.ErrReader(io.ErrUnexpectedEOF))
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func newMetricStatsFixture(t testing.TB, devices int, data []byte) (FS, string) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "proc"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(root, "sys"), 0o755))
	for i := range devices {
		path := filepath.Join(root, "sys/fs/xfs", fmt.Sprintf("sda%d", i), "stats")
		require.NoError(t, os.MkdirAll(path, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(path, "stats"), data, 0o600))
	}

	originalPrefix := filepath.Dir(procfs.DefaultPath())
	procfs.RootPrefix(root)
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })
	fs, err := NewDefaultFS()
	require.NoError(t, err)
	return fs, root
}

func TestReadDeviceStats(t *testing.T) {
	data := []byte("extent_alloc 1 2 3 4\ngc xpc 18446744073709551615\n")
	fs, root := newMetricStatsFixture(t, 2, data)
	// Both readers must retain their original mount after the default changes.
	procfs.RootPrefix(t.TempDir())
	stats, err := fs.ReadDeviceStats()
	require.NoError(t, err)
	require.Len(t, stats, 2)
	for i, stat := range stats {
		assert.Equal(t, fmt.Sprintf("sda%d", i), stat.Name)
		assert.Equal(t, uint32(2), stat.ExtentAllocation.BlocksAllocated)
	}
	_, err = fs.SysStats()
	assert.Error(t, err)

	path := filepath.Join(root, "sys/fs/xfs/sda0/stats/stats")
	require.NoError(t, os.WriteFile(path, []byte("extent_alloc invalid\n"), 0o600))
	stats, err = fs.ReadDeviceStats()
	assert.ErrorContains(t, err, path)
	assert.Nil(t, stats)

	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Mkdir(path, 0o755))
	stats, err = fs.ReadDeviceStats()
	assert.ErrorContains(t, err, path)
	assert.Nil(t, stats)
}

func TestReadDeviceStatsNoDevices(t *testing.T) {
	fs, _ := newMetricStatsFixture(t, 0, nil)
	stats, err := fs.ReadDeviceStats()
	require.NoError(t, err)
	assert.Empty(t, stats)
}

func BenchmarkReadDeviceStats(b *testing.B) {
	data, err := os.ReadFile("../../../integration/fixtures/sys/fs/xfs/sda0/stats/stats")
	require.NoError(b, err)
	for _, devices := range []int{1, 16, 128} {
		b.Run(fmt.Sprintf("devices=%d", devices), func(b *testing.B) {
			fs, _ := newMetricStatsFixture(b, devices, data)
			for _, reader := range []struct {
				name string
				read func() ([]*Stats, error)
			}{
				{
					name: "all",
					read: fs.SysStats,
				},
				{
					name: "metrics",
					read: fs.ReadDeviceStats,
				},
			} {
				b.Run(reader.name, func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						if _, err := reader.read(); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		})
	}
}
