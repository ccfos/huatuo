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

package netutil

import (
	"os"
	"testing"
)

func TestNetNSInodeByPid(t *testing.T) {
	tests := []struct {
		name    string
		pid     int
		wantErr bool
	}{
		{
			name:    "valid current pid",
			pid:     os.Getpid(),
			wantErr: false,
		},
		{
			name:    "invalid pid 0",
			pid:     0,
			wantErr: true,
		},
		{
			name:    "invalid negative pid",
			pid:     -1,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NetNSInodeByPid(tt.pid)
			if (err != nil) != tt.wantErr {
				t.Errorf("NetNSInodeByPid() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got == 0 {
				t.Errorf("NetNSInodeByPid() got = %v, want non-zero inode", got)
			}
		})
	}
}
