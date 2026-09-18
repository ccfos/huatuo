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

package collector

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseNetStat(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    map[string]map[string]string
		wantErr string
	}{
		{
			name:    "valid with repeated whitespace",
			content: "TcpExt: SyncookiesSent  SyncookiesRecv\nTcpExt: 1  2\nTcp: ActiveOpens\nTcp: 3\n",
			want: map[string]map[string]string{
				"TcpExt": {"SyncookiesSent": "1", "SyncookiesRecv": "2"},
				"Tcp":    {"ActiveOpens": "3"},
			},
		},
		{
			name:    "missing value row",
			content: "Tcp: ActiveOpens\n",
			wantErr: "missing values for Tcp:",
		},
		{
			name:    "mismatched protocols",
			content: "Tcp: ActiveOpens\nUdp: 3\n",
			wantErr: "mismatched netstat rows",
		},
		{
			name:    "malformed header",
			content: "Tcp ActiveOpens\nTcp: 3\n",
			wantErr: "invalid netstat header",
		},
		{
			name:    "oversized header",
			content: "Tcp: " + strings.Repeat("A", bufio.MaxScanTokenSize) + "\nTcp: 1\n",
			wantErr: "scan netstat header",
		},
		{
			name:    "oversized values",
			content: "Tcp: ActiveOpens\nTcp: " + strings.Repeat("1", bufio.MaxScanTokenSize) + "\n",
			wantErr: "scan netstat values",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "netstat")
			require.NoError(t, os.WriteFile(path, []byte(tt.content), 0o600))

			got, err := parseNetStat(path)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
