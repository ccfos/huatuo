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

package memsnap

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestOpenMappedFileInodeValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mapped")
	if err := os.WriteFile(path, []byte("ELF fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stat unix.Stat_t
	if err := unix.Stat(path, &stat); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		inode   uint64
		wantErr bool
	}{
		{"different device", stat.Ino, false},
		{"missing inode", 0, true},
		{"different inode", stat.Ino + 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, err := OpenMappedFile(path, ProcMap{
				Inode:    tc.inode,
				DevMajor: unix.Major(stat.Dev) + 1, DevMinor: unix.Minor(stat.Dev) + 1,
			})
			if file != nil {
				defer file.Close()
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("error=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
	if _, err := OpenMappedFile(filepath.Dir(path), ProcMap{Inode: stat.Ino}); err == nil {
		t.Fatal("accepted a directory")
	}
}
