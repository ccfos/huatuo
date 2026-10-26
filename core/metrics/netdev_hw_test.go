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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/procfs"
)

func TestNewNetdevHwClosesEthtoolSocket(t *testing.T) {
	previousRoot := filepath.Dir(procfs.DefaultPath())
	previousConfig := configSnapshot()
	t.Cleanup(func() {
		procfs.RootPrefix(previousRoot)
		Set(previousConfig)
	})
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sys", "class", "net"), 0o755); err != nil {
		t.Fatal(err)
	}
	procfs.RootPrefix(root)

	for _, invalidFilter := range []bool{false, true} {
		name := "success"
		cfg := &Config{}
		if invalidFilter {
			name = "invalid_filter"
			cfg.NetdevHW.DeviceList = []string{"["}
		}
		t.Run(name, func(t *testing.T) {
			Set(cfg)
			before := openSocketCount(t)
			for range 8 {
				_, err := newNetdevHw()
				if (err != nil) != invalidFilter {
					t.Fatalf("constructor error = %v, invalid filter = %v", err, invalidFilter)
				}
			}
			if after := openSocketCount(t); after != before {
				t.Fatalf("constructor retained sockets: before=%d after=%d", before, after)
			}
		})
	}
}

func openSocketCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if err == nil && strings.HasPrefix(target, "socket:[") {
			count++
		}
	}
	return count
}
