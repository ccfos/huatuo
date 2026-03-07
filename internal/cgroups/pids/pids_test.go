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
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// writeProcFile writes content into dir/filename with 0600 permissions and
// returns the directory path for use as the Tasks `path` argument.
func writeProcFile(t *testing.T, filename, content string) string {
	t.Helper()
	dir := t.TempDir()
	dst := filepath.Join(dir, filename)
	if err := os.WriteFile(dst, []byte(content), 0o600); err != nil {
		t.Fatalf("writeProcFile: %v", err)
	}
	return dir
}

// TestTasks covers the primary read and parse paths of Tasks.
func TestTasks(t *testing.T) {
	cases := []struct {
		name    string
		file    string
		content string
		want    []int32
		wantErr bool
	}{
		{
			name:    "empty-file-returns-nil-slice",
			file:    "cgroup.procs",
			content: "",
			want:    nil,
		},
		{
			name:    "single-pid-cgroup-procs",
			file:    "cgroup.procs",
			content: "1001\n",
			want:    []int32{1001},
		},
		{
			// Realistic thread list for a huatuo-dev workload.
			name:    "multiple-pids-cgroup-threads",
			file:    "cgroup.threads",
			content: "2001\n2002\n2003\n",
			want:    []int32{2001, 2002, 2003},
		},
		{
			// Some kernels append a trailing newline; the blank line must be ignored.
			name:    "trailing-blank-lines-are-skipped",
			file:    "cgroup.procs",
			content: "3001\n\n3002\n\n",
			want:    []int32{3001, 3002},
		},
		{
			name:    "non-numeric-line-returns-parse-error",
			file:    "cgroup.procs",
			content: "4001\nnot-a-pid\n4002\n",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeProcFile(t, tc.file, tc.content)
			got, err := Tasks(dir, tc.file)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Tasks: got err %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && !slices.Equal(got, tc.want) {
				t.Errorf("Tasks: got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestTasks_FileNotFound verifies that Tasks propagates os.Open errors when
// the target cgroup file does not exist.
func TestTasks_FileNotFound(t *testing.T) {
	dir := t.TempDir() // empty — no cgroup.procs written
	got, err := Tasks(dir, "cgroup.procs")
	if err == nil {
		t.Fatalf("Tasks: expected error for missing file, got nil (pids: %v)", got)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Tasks: got error %v, want os.ErrNotExist", err)
	}
	if got != nil {
		t.Errorf("Tasks: got %v on error, want nil", got)
	}
}
