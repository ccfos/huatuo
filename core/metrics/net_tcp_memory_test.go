// Copyright 2025, 2026 The HuaTuo Authors
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

package collector

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"huatuo-bamai/internal/procfs"
)

func TestParseTcpMemoryBoundary(t *testing.T) {
	tempDir := t.TempDir()
	sysctlDir := filepath.Join(tempDir, "proc", "sys", "net", "ipv4")
	require.NoError(t, os.MkdirAll(sysctlDir, 0o755))

	tcpMemPath := filepath.Join(sysctlDir, "tcp_mem")

	procfs.RootPrefix(tempDir)
	defer procfs.RootPrefix("/")

	tests := []struct {
		name      string
		content   string
		wantErr   bool
		wantInErr string
	}{
		{"empty_file", "", true, "tcp_mem"},
		{"one_value", "100", true, "tcp_mem"},
		{"two_values", "100 200", true, "tcp_mem"},
		// With 3+ values the guard passes; NetSockstat will fail because
		// no sockstat file exists in the temp procfs, but the error must
		// NOT be the tcp_mem guard error.
		{"three_values_passes_guard", "100 200 300", true, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := os.WriteFile(tcpMemPath, []byte(tt.content), 0o600)
			require.NoError(t, err)

			_, err = parseTcpMemory()
			require.Error(t, err)
			if tt.wantInErr != "" {
				require.True(t, strings.Contains(err.Error(), tt.wantInErr))
			} else {
				require.False(t, strings.Contains(err.Error(), "tcp_mem"))
			}
		})
	}
}
