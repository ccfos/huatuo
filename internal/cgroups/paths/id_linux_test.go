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

package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKernfsIDSymlink(t *testing.T) {
	root := os.Getenv("HUATUO_KERNFS_TEST_PATH")
	if root == "" {
		t.Skip("set HUATUO_KERNFS_TEST_PATH to a cgroup directory")
	}
	want, err := KernfsID(root)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "cgroup")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	got, err := KernfsID(alias)
	if err != nil || got != want || got == 0 {
		t.Fatalf("ID=%d, want %d, err=%v", got, want, err)
	}
}
