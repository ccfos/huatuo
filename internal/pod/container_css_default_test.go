// Copyright 2025, 2026 The HuaTuo Authors
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

package pod

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractContainerID(t *testing.T) {
	for _, tc := range []struct {
		input    string
		expected string
	}{
		{ // docker container cgroup name
			input:    "c2b95e61271060bef9a8b832e50c81f5eed60b788ff8a42b173c4a694c284a77",
			expected: "c2b95e61271060bef9a8b832e50c81f5eed60b788ff8a42b173c4a694c284a77",
		},
		{ // docker pod cgroup name
			input:    "pod66384b12-8f16-45f5-b520-f378e0f491fe",
			expected: "",
		},
		{ // containerd pod cgroup name
			input:    "kubepods-burstable-pod44e9d203_d0d2_4d44_a5da_702190080eb4.slice",
			expected: "",
		},
		{ // containerd container cgroup name
			input:    "cri-containerd-bd23762346b2af6261d285e8c2bdf82f9abeb427338c086cca27da98fee4dfa5.scope",
			expected: "bd23762346b2af6261d285e8c2bdf82f9abeb427338c086cca27da98fee4dfa5",
		},
	} {
		actual := extractContainerID(tc.input)
		if actual != tc.expected {
			t.Errorf("parseContainerID input %s want %s  actual %s", tc.input, tc.expected, actual)
		}
	}
}

func TestResolveCgroupFilesystemPath(t *testing.T) {
	realRoot := t.TempDir()
	symlinkParent := t.TempDir()
	symlinkRoot := filepath.Join(symlinkParent, "cpu")
	if err := os.Symlink(realRoot, symlinkRoot); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	membershipPath := "/kubepods/container"
	cgroupPath := filepath.Join(realRoot, "kubepods", "container")
	if err := os.MkdirAll(cgroupPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(cgroupPath, cgroupv1NotifyFile), nil, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := resolveCgroupFilesystemPath(symlinkRoot, membershipPath, cgroupv1NotifyFile)
	if err != nil {
		t.Fatalf("resolveCgroupFilesystemPath() error = %v", err)
	}
	if got != cgroupPath {
		t.Fatalf("resolveCgroupFilesystemPath() = %q, want %q", got, cgroupPath)
	}
}

func TestResolveCgroupFilesystemPathRejectsMissingNotificationFile(t *testing.T) {
	root := t.TempDir()
	cgroupPath := filepath.Join(root, "kubepods", "container")
	if err := os.MkdirAll(cgroupPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	_, err := resolveCgroupFilesystemPath(root, "/kubepods/container", cgroupv2NotifyFile)
	if err == nil {
		t.Fatal("resolveCgroupFilesystemPath() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), filepath.Join(cgroupPath, cgroupv2NotifyFile)) {
		t.Fatalf("resolveCgroupFilesystemPath() error = %q, want notification path", err)
	}
}
