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

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ccfos/huatuo/internal/procfs"
)

func TestBuildProcessFileIOStatsCommandFallback(t *testing.T) {
	tests := []struct {
		name    string
		cmdline string
		missing bool
		want    string
	}{
		{name: "empty kernel thread command", want: "kworker/0:1"},
		{name: "exited process", missing: true, want: "kworker/0:1"},
		{name: "userspace command", cmdline: "worker\x00--batch\x00", want: "worker --batch "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			procfs.RootPrefix(root)
			t.Cleanup(func() { procfs.RootPrefix("") })
			if !tt.missing {
				path := filepath.Join(root, "proc", "4242", "cmdline")
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(tt.cmdline), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			record := &bpfFilesystemIO{TGID: 4242, FsReadBytes: 80}
			copy(record.Comm[:], "kworker/0:1")
			got := buildProcessFileIOStats(&pidGroup{PID: 4242, Files: []*fileEntry{{Record: record}}}, ioConfig{durationSecond: 8, maxFilesPerProcess: 1})
			if got.Comm != tt.want {
				t.Fatalf("Comm = %q, want %q", got.Comm, tt.want)
			}
		})
	}
}
