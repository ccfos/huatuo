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

package pids

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSubtreeIncludesDescendantsAndDeduplicates(t *testing.T) {
	root := t.TempDir()
	for dir, data := range map[string]string{"": "100\n", "worker": "200\n100\n", "worker/nested": "300\n", "empty": ""} {
		path := filepath.Join(root, dir)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "cgroup.procs"), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "cgroup.procs"), []byte("999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	got, err := Subtree(root, "cgroup.procs")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []int32{100, 200, 300}) {
		t.Fatalf("PIDs=%v, want [100 200 300]", got)
	}
}

func TestSubtreeSkipsDisappearedChildButReportsRootErrors(t *testing.T) {
	root := writeProcFile(t, "cgroup.procs", "100\n")
	if err := os.Mkdir(filepath.Join(root, "gone"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := Subtree(root, "cgroup.procs")
	if err != nil || !reflect.DeepEqual(got, []int32{100}) {
		t.Fatalf("PIDs=%v, error=%v", got, err)
	}
	if _, err := Subtree(filepath.Join(root, "missing"), "cgroup.procs"); err == nil {
		t.Fatal("missing root was accepted")
	}
	if err := os.WriteFile(filepath.Join(root, "cgroup.procs"), []byte("invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Subtree(root, "cgroup.procs"); err == nil {
		t.Fatal("malformed PID was ignored")
	}
}
