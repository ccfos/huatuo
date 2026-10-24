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

func TestParseTCPMemoryRejectsShortSysctl(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "proc", "sys", "net", "ipv4", "tcp_mem")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	originalPrefix := filepath.Dir(procfs.DefaultPath())
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })

	for _, tt := range []struct {
		name    string
		content string
	}{
		{name: "empty", content: ""},
		{name: "one value", content: "1\n"},
		{name: "two values", content: "1 2\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}
			procfs.RootPrefix(root)
			if _, err := parseTcpMemory(); err == nil || !strings.Contains(err.Error(), "want at least 3") {
				t.Fatalf("parseTcpMemory() error = %v, want short-value error", err)
			}
		})
	}
}

func TestTCPMemoryLimitRequiresThreeValues(t *testing.T) {
	for _, values := range [][]int{nil, {1}, {1, 2}} {
		_, err := tcpMemoryLimit(values)
		if err == nil || !strings.Contains(err.Error(), "want at least 3") {
			t.Fatalf("tcpMemoryLimit(%v) error = %v, want short-value error", values, err)
		}
	}
}

func TestTCPMemoryLimitUsesThirdValue(t *testing.T) {
	got, err := tcpMemoryLimit([]int{1, 2, 3})
	if err != nil {
		t.Fatalf("tcpMemoryLimit() error = %v", err)
	}
	if got != 3 {
		t.Fatalf("tcpMemoryLimit() = %v, want 3", got)
	}
}
