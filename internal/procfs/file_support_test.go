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
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestRequireFile(t *testing.T) {
	tests := []struct {
		name string
		path []string
	}{
		{name: "ARP cache", path: []string{"net/stat/arp_cache"}},
		{name: "TCP memory", path: []string{"sys/net/ipv4/tcp_mem"}},
	}

	originalPrefix := filepath.Dir(DefaultPath())
	t.Cleanup(func() { RootPrefix(originalPrefix) })

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			RootPrefix(root)

			if err := RequireFile(tt.path...); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("RequireFile() error = %v, want ErrNotExist", err)
			}

			path := Path(tt.path...)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatalf("create procfs directory: %v", err)
			}
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatalf("create procfs file: %v", err)
			}

			if err := RequireFile(tt.path...); err != nil {
				t.Fatalf("RequireFile() error = %v, want nil", err)
			}
		})
	}
}
