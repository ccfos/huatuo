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

package golang

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

func TestExecutableMappingIdentity(t *testing.T) {
	path := "/bin/heap (deleted)"
	exe := filepath.Join(t.TempDir(), "exe")
	if err := os.Symlink(path, exe); err != nil {
		t.Fatal(err)
	}
	mappings := []memsnapshot.ProcMap{
		{Path: "/lib/other", Inode: 42, DevMajor: 8, DevMinor: 2, Start: 0x1000},
		{Path: path, Inode: 42, DevMajor: 8, DevMinor: 3, Start: 0x8000},
	}
	mapping, err := executableMapping(exe, 42, mappings)
	if err != nil || mapping != &mappings[1] {
		t.Fatalf("executable identity = %+v, %v", mapping, err)
	}
	if _, err := executableMapping(exe, 42, mappings[:1]); err == nil {
		t.Fatal("accepted an unrelated executable path")
	}
	if _, err := executableMapping(exe, 43, mappings); err == nil {
		t.Fatal("accepted a replaced executable inode")
	}
	mappings[0].Path = path
	if _, err := executableMapping(exe, 42, mappings); err == nil {
		t.Fatal("accepted an ambiguous executable device")
	}
}
