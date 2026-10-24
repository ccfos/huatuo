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
	"syscall"
	"testing"
	"time"
)

type testFileInfo struct{ sys any }

func (f testFileInfo) Name() string       { return "net" }
func (f testFileInfo) Size() int64        { return 0 }
func (f testFileInfo) Mode() os.FileMode  { return 0 }
func (f testFileInfo) ModTime() time.Time { return time.Time{} }
func (f testFileInfo) IsDir() bool        { return false }
func (f testFileInfo) Sys() any           { return f.sys }

func TestNetNamespaceInumByPID(t *testing.T) {
	tests := []struct {
		name    string
		pid     int
		wantErr bool
	}{
		{name: "valid current pid", pid: os.Getpid()},
		{name: "invalid pid 0", pid: 0, wantErr: true},
		{name: "invalid negative pid", pid: -1, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NetNamespaceInumByPID(tt.pid)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NetNamespaceInumByPID() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got == 0 {
				t.Errorf("NetNamespaceInumByPID() got = %v, want non-zero inum", got)
			}
		})
	}
}

func TestNetNamespaceInumChecksStatMetadata(t *testing.T) {
	for _, info := range []os.FileInfo{nil, testFileInfo{sys: "unexpected"}, testFileInfo{}} {
		if _, err := netNamespaceInum(info); err == nil {
			t.Fatalf("netNamespaceInum(%T) error = nil, want metadata error", info)
		}
	}
}

func TestNetNamespaceInumReturnsInode(t *testing.T) {
	got, err := netNamespaceInum(testFileInfo{sys: &syscall.Stat_t{Ino: 42}})
	if err != nil {
		t.Fatalf("netNamespaceInum() error = %v", err)
	}
	if got != 42 {
		t.Fatalf("netNamespaceInum() = %d, want 42", got)
	}
}
