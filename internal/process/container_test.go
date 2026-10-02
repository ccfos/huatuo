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

package process

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestIsInContainer(t *testing.T) {
	tests := []struct {
		name   string
		cgroup string
		want   bool
	}{
		{
			name:   "host process",
			cgroup: "0::/user.slice\n",
			want:   false,
		},
		{
			name:   "container cgroup",
			cgroup: "0::/kubepods.slice/kubepods-burstable.slice\n",
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpRoot := setProcRoot(t)
			cgroupPath := filepath.Join(tmpRoot, "proc", "100", "cgroup")
			if err := os.MkdirAll(filepath.Dir(cgroupPath), 0o755); err != nil {
				t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(cgroupPath), err)
			}
			if err := os.WriteFile(cgroupPath, []byte(tt.cgroup), 0o600); err != nil {
				t.Fatalf("WriteFile(%q) error = %v", cgroupPath, err)
			}

			inContainer, err := IsInContainer(100)
			if err != nil {
				t.Fatalf("IsInContainer(100) error = %v", err)
			}
			if inContainer != tt.want {
				t.Fatalf("IsInContainer(100) = %t, want %t", inContainer, tt.want)
			}
		})
	}
}

func TestIsInContainerMissingProcess(t *testing.T) {
	tmpRoot := setProcRoot(t)
	procPath := filepath.Join(tmpRoot, "proc")
	if err := os.MkdirAll(procPath, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", procPath, err)
	}

	if _, err := IsInContainer(100); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("IsInContainer(100) error = %v, want fs.ErrNotExist", err)
	}
}

func TestHasDifferentMountNamespace(t *testing.T) {
	tests := []struct {
		name             string
		hostNamespace    string
		processNamespace string
		want             bool
	}{
		{
			name:             "same namespace",
			hostNamespace:    "mnt:[1]",
			processNamespace: "mnt:[1]",
			want:             false,
		},
		{
			name:             "different namespace",
			hostNamespace:    "mnt:[1]",
			processNamespace: "mnt:[2]",
			want:             true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpRoot := setProcRoot(t)
			writeMountNamespace(t, tmpRoot, 1, tt.hostNamespace)
			writeMountNamespace(t, tmpRoot, 100, tt.processNamespace)

			got, err := HasDifferentMountNamespace(100)
			if err != nil {
				t.Fatalf("HasDifferentMountNamespace(100) error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("HasDifferentMountNamespace(100) = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestIsContainerCgroupPath(t *testing.T) {
	containerID := strings.Repeat("a", 64)
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "kubernetes cgroupfs", path: "/kubepods/burstable/pod123", want: true},
		{name: "kubernetes systemd", path: "/kubepods-burstable-pod123.slice", want: true},
		{name: "docker cgroupfs", path: "/docker/" + containerID, want: true},
		{name: "docker systemd", path: "/system.slice/docker-" + containerID + ".scope", want: true},
		{name: "containerd systemd", path: "/system.slice/cri-containerd-" + containerID + ".scope", want: true},
		{name: "crio systemd", path: "/system.slice/crio-" + containerID + ".scope", want: true},
		{name: "podman systemd", path: "/user.slice/libpod-" + containerID + ".scope", want: true},
		{name: "containerd daemon", path: "/system.slice/containerd.service", want: false},
		{name: "docker daemon", path: "/system.slice/docker.service", want: false},
		{name: "short docker scope", path: "/system.slice/docker-abcd.scope", want: false},
		{name: "host process", path: "/user.slice/user-1000.slice/session-2.scope", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isContainerCgroupPath(tt.path); got != tt.want {
				t.Fatalf("isContainerCgroupPath(%q) = %t, want %t", tt.path, got, tt.want)
			}
		})
	}
}

func writeMountNamespace(t *testing.T, root string, pid int, namespace string) {
	t.Helper()
	namespacePath := filepath.Join(root, "proc", strconv.Itoa(pid), "ns")
	if err := os.MkdirAll(namespacePath, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", namespacePath, err)
	}
	if err := os.Symlink(namespace, filepath.Join(namespacePath, "mnt")); err != nil {
		t.Fatalf("Symlink(%q) error = %v", namespacePath, err)
	}
}
