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

func TestDefaultNetClassDevices_Filesystem(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(t *testing.T) string
		want        []string
		wantErr     bool
		errContains string
	}{
		{
			name: "multiple devices",
			setup: func(t *testing.T) string {
				tmpDir := t.TempDir()
				netPath := filepath.Join(tmpDir, "sys", "class", "net")
				require.NoError(t, os.MkdirAll(netPath, 0o755))

				for _, dev := range []string{"eth0", "eth1", "lo"} {
					require.NoError(t, os.Mkdir(filepath.Join(netPath, dev), 0o755))
				}
				return tmpDir
			},
			want:    []string{"eth0", "eth1", "lo"},
			wantErr: false,
		},
		{
			name: "no devices",
			setup: func(t *testing.T) string {
				tmpDir := t.TempDir()
				netPath := filepath.Join(tmpDir, "sys", "class", "net")
				require.NoError(t, os.MkdirAll(netPath, 0o755))
				return tmpDir
			},
			want:    []string{},
			wantErr: false,
		},
		{
			name: "ignores regular files",
			setup: func(t *testing.T) string {
				tmpDir := t.TempDir()
				netPath := filepath.Join(tmpDir, "sys", "class", "net")
				require.NoError(t, os.MkdirAll(netPath, 0o755))
				require.NoError(t, os.Mkdir(filepath.Join(netPath, "eth0"), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(netPath, "file.txt"), []byte("test"), 0o600))
				return tmpDir
			},
			want:    []string{"eth0"},
			wantErr: false,
		},
		{
			name: "device names with special characters",
			setup: func(t *testing.T) string {
				tmpDir := t.TempDir()
				netPath := filepath.Join(tmpDir, "sys", "class", "net")
				require.NoError(t, os.MkdirAll(netPath, 0o755))

				specialDevs := []string{"eth0.1", "vlan@100", "br-123456"}
				for _, dev := range specialDevs {
					require.NoError(t, os.Mkdir(filepath.Join(netPath, dev), 0o755))
				}
				return tmpDir
			},
			want:    []string{"eth0.1", "vlan@100", "br-123456"},
			wantErr: false,
		},
	}

	originalPrefix := strings.TrimSuffix(DefaultPath(), "sys")
	defer func() { procfs.RootPrefix(originalPrefix) }()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			procfs.RootPrefix(tt.setup(t))

			devices, err := DefaultNetClassDevices()

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.ElementsMatch(t, tt.want, devices)
			}
		})
	}
}

func TestDefaultNetClass_Filesystem(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T) string
		wantErr  bool
		validate func(*testing.T, NetClass)
	}{
		{
			name: "valid net class with devices",
			setup: func(t *testing.T) string {
				tmpDir := t.TempDir()
				sysPath := filepath.Join(tmpDir, "sys")

				// Create eth0 device with attributes
				eth0Path := filepath.Join(sysPath, "class", "net", "eth0")
				require.NoError(t, os.MkdirAll(eth0Path, 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(eth0Path, "address"), []byte("00:11:22:33:44:55"), 0o600))
				require.NoError(t, os.WriteFile(filepath.Join(eth0Path, "mtu"), []byte("1500"), 0o600))
				require.NoError(t, os.WriteFile(filepath.Join(eth0Path, "operstate"), []byte("up"), 0o600))

				// Create lo device with attributes
				loPath := filepath.Join(sysPath, "class", "net", "lo")
				require.NoError(t, os.MkdirAll(loPath, 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(loPath, "address"), []byte("00:00:00:00:00:00"), 0o600))
				require.NoError(t, os.WriteFile(filepath.Join(loPath, "mtu"), []byte("65536"), 0o600))
				require.NoError(t, os.WriteFile(filepath.Join(loPath, "operstate"), []byte("unknown"), 0o600))

				return tmpDir
			},
			validate: func(t *testing.T, nc NetClass) {
				assert.NotNil(t, nc)

				// Verify eth0 exists
				eth0, ok := nc["eth0"]
				assert.True(t, ok, "eth0 should exist in NetClass")
				assert.Equal(t, "eth0", eth0.Name)
				assert.Equal(t, "00:11:22:33:44:55", eth0.Address)
				assert.NotNil(t, eth0.MTU)
				assert.Equal(t, int64(1500), *eth0.MTU)
				assert.Equal(t, "up", eth0.OperState)

				// Verify eth0.Speed is not exist
				assert.Nil(t, eth0.Speed)

				// Verify lo exists
				lo, ok := nc["lo"]
				assert.True(t, ok, "lo should exist in NetClass")
				assert.Equal(t, "lo", lo.Name)
				assert.Equal(t, "00:00:00:00:00:00", lo.Address)
				assert.NotNil(t, lo.MTU)
				assert.Equal(t, int64(65536), *lo.MTU)
				assert.Equal(t, "unknown", lo.OperState)

				// Verify we have 2 devices
				assert.Len(t, nc, 2)
			},
		},
		{
			name: "empty net class",
			setup: func(t *testing.T) string {
				tmpDir := t.TempDir()
				sysPath := filepath.Join(tmpDir, "sys")
				netPath := filepath.Join(sysPath, "class", "net")
				require.NoError(t, os.MkdirAll(netPath, 0o755))
				return tmpDir
			},
			validate: func(t *testing.T, nc NetClass) {
				assert.NotNil(t, nc)
				assert.Empty(t, nc, "NetClass should be empty when no devices exist")
			},
		},
	}

	originalPrefix := strings.TrimSuffix(DefaultPath(), "sys")
	defer func() { procfs.RootPrefix(originalPrefix) }()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			procfs.RootPrefix(tt.setup(t))

			netClass, err := DefaultNetClass()
			assert.NoError(t, err)
			tt.validate(t, netClass)
		})
	}
}

// Integration Tests (Real Environment)
// TEST_INTEGRATION=true go test -v ./internal/procfs/sysfs/...
func TestDefaultNetClassDevices_Integration(t *testing.T) {
	if os.Getenv("TEST_INTEGRATION") == "" {
		t.Skip("Set TEST_INTEGRATION=true to run integration tests")
	}

	devices, err := DefaultNetClassDevices()
	if err != nil {
		t.Skipf("/sys/class/net not available: %v", err)
	}

	assert.NotEmpty(t, devices, "should have at least one network device")
	t.Logf("Found %d devices: %v", len(devices), devices)
}

func TestDefaultNetClass_Integration(t *testing.T) {
	if os.Getenv("TEST_INTEGRATION") == "" {
		t.Skip("Set TEST_INTEGRATION=true to run integration tests")
	}

	netClass, err := DefaultNetClass()
	if err != nil {
		t.Skipf("/sys/class/net not available: %v", err)
	}

	assert.NotNil(t, netClass, "should have a valid NetClass object")
	t.Logf("Got NetClass: %+v", netClass)
}
