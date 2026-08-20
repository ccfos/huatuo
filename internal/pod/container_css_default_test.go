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
	"encoding/binary"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"huatuo-bamai/internal/cgroups/subsystem"

	"github.com/cilium/ebpf/btf"
)

func TestCgroupSubSysIDNameMap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		values    []btf.EnumValue
		want      map[int]string
		wantError string
	}{
		{
			name: "maps valid values and ignores unrelated or out-of-range values",
			values: []btf.EnumValue{
				{Name: "cpuset_cgrp_id", Value: 0},
				{Name: "cpu_cgrp_id", Value: 1},
				{Name: "unrelated", Value: 2},
				{Name: "io_cgrp_id", Value: 3},
				{Name: "memory_cgrp_id", Value: 4},
				{Name: "rdma_cgrp_id", Value: 12},
				{Name: "CGROUP_SUBSYS_COUNT", Value: 13},
				{Name: "future_cgrp_id", Value: 13},
			},
			want: map[int]string{
				0:  "cpuset",
				1:  "cpu",
				3:  subsystem.SubsystemBlkIO,
				4:  "memory",
				12: "rdma",
			},
		},
		{
			name:   "accepts a sparse kernel configuration",
			values: []btf.EnumValue{{Name: "memory_cgrp_id", Value: 4}},
			want:   map[int]string{4: "memory"},
		},
		{
			name: "rejects no subsystem values",
			values: []btf.EnumValue{
				{Name: "CGROUP_SUBSYS_COUNT", Value: 13},
				{Name: "future_cgrp_id", Value: 13},
			},
			wantError: "cgroup_subsys_id has no subsystem values",
		},
		{
			name: "rejects duplicate IDs",
			values: []btf.EnumValue{
				{Name: "cpu_cgrp_id", Value: 1},
				{Name: "memory_cgrp_id", Value: 1},
			},
			wantError: `cgroup subsystem id 1 maps to both "cpu" and "memory"`,
		},
		{
			name: "rejects duplicate normalized names",
			values: []btf.EnumValue{
				{Name: "io_cgrp_id", Value: 3},
				{Name: "blkio_cgrp_id", Value: 4},
			},
			wantError: `cgroup subsystem "blkio" maps to both ids 3 and 4`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := cgroupSubSysIDNameMap(tt.values)
			if tt.wantError != "" {
				if err == nil {
					t.Fatalf("cgroupSubSysIDNameMap() error = nil, want %q", tt.wantError)
				}
				if !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("cgroupSubSysIDNameMap() error = %q, want %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("cgroupSubSysIDNameMap() error = %v", err)
			}
			if !maps.Equal(got, tt.want) {
				t.Errorf("cgroupSubSysIDNameMap() = %v, want %v", got, tt.want)
			}
		})
	}
}

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

func TestKernfsNodeIDOffset(t *testing.T) {
	for _, typ := range []btf.Type{
		&btf.Int{Name: "u64", Size: 8},
		&btf.Union{Name: "kernfs_node_id", Size: 8},
	} {
		offset, err := kernfsNodeIDOffset(&btf.Struct{Members: []btf.Member{{
			Name:   "id",
			Type:   typ,
			Offset: 192,
		}}})
		if err != nil || offset != 24 {
			t.Fatalf("kernfsNodeIDOffset() = %d, %v; want 24, nil", offset, err)
		}
	}
}

func TestKernfsNodeIDOffsetFromTypes(t *testing.T) {
	newKernfsNode := func(offset btf.Bits) *btf.Struct {
		return &btf.Struct{Members: []btf.Member{{
			Name:   "id",
			Type:   &btf.Union{Name: "kernfs_node_id", Size: 8},
			Offset: offset,
		}}}
	}

	offset, err := kernfsNodeIDOffsetFromTypes([]btf.Type{
		newKernfsNode(192),
		newKernfsNode(192),
	})
	if err != nil || offset != 24 {
		t.Fatalf("kernfsNodeIDOffsetFromTypes() = %d, %v; want 24, nil", offset, err)
	}

	_, err = kernfsNodeIDOffsetFromTypes([]btf.Type{
		newKernfsNode(192),
		newKernfsNode(256),
	})
	if err == nil {
		t.Fatal("kernfsNodeIDOffsetFromTypes() error = nil, want non-nil")
	}
}

func TestCgroup2PathOnMount(t *testing.T) {
	mountInfo := "36 25 0:32 / /sys/fs/cgroup rw,nosuid,nodev,noexec,relatime - cgroup2 cgroup rw\n" +
		"37 25 0:33 / /sys/fs/cgroup/cpu rw,nosuid,nodev,noexec,relatime - cgroup cgroup rw,cpu\n" //nolint:dupword // v1 fstype and mount source are both "cgroup"

	path, err := cgroup2PathOnMount(mountInfo, "/kubepods/pod/container")
	if err != nil {
		t.Fatalf("cgroup2PathOnMount: %v", err)
	}
	if want := "/sys/fs/cgroup/kubepods/pod/container"; path != want {
		t.Fatalf("cgroup2 path = %q, want %q", path, want)
	}
}

func TestCgroup2PathOnMountWithSubtreeRoot(t *testing.T) {
	mountInfo := "36 25 0:32 /delegated /cgroup2 rw,nosuid,nodev,noexec,relatime - cgroup2 cgroup rw\n"

	path, err := cgroup2PathOnMount(mountInfo, "/delegated/pod/container")
	if err != nil {
		t.Fatalf("cgroup2PathOnMount: %v", err)
	}
	if want := "/cgroup2/pod/container"; path != want {
		t.Fatalf("cgroup2 path = %q, want %q", path, want)
	}

	if _, err := cgroup2PathOnMount(mountInfo, "/other/container"); err == nil {
		t.Fatal("path outside cgroup2 mount root must fail")
	}
}

func TestCgroupIDFromHandleBytes(t *testing.T) {
	handle := make([]byte, cgroupV2HandleSize)
	const want uint64 = 0x1020304050607080
	binary.NativeEndian.PutUint64(handle, want)

	got, err := cgroupIDFromHandleBytes(handle)
	if err != nil || got != want {
		t.Fatalf("cgroupIDFromHandleBytes() = (%#x, %v), want (%#x, nil)", got, err, want)
	}

	if _, err := cgroupIDFromHandleBytes(handle[:cgroupV2HandleSize-1]); err == nil {
		t.Fatal("cgroupIDFromHandleBytes() error = nil, want non-nil for a short handle")
	}
}

func TestBuildContainerCgroupKeys(t *testing.T) {
	containers := map[string]*Container{
		"v1": {ID: "v1", CgroupCss: map[string]uint64{subsystem.SubsystemMemory: 201}},
		"v2": {ID: "v2", CgroupID: 102, CgroupCss: map[string]uint64{}},
	}

	keys := BuildContainerCgroupKeys(containers, subsystem.SubsystemMemory)
	for key, want := range map[ContainerCgroupKey]string{
		{CSS: 201}:      "v1",
		{CgroupID: 102}: "v2",
	} {
		if got := keys[key]; got == nil || got.ID != want {
			t.Fatalf("key %+v = %+v, want container %s", key, got, want)
		}
	}
}

func TestBuildContainerCgroupKeysDropsDuplicateKey(t *testing.T) {
	containers := map[string]*Container{
		"first":  {ID: "first", CgroupID: 101, CgroupCss: map[string]uint64{subsystem.SubsystemMemory: 301}},
		"second": {ID: "second", CgroupID: 101, CgroupCss: map[string]uint64{subsystem.SubsystemMemory: 302}},
		"third":  {ID: "third", CgroupID: 103, CgroupCss: map[string]uint64{subsystem.SubsystemMemory: 301}},
	}

	keys := BuildContainerCgroupKeys(containers, subsystem.SubsystemMemory)

	for _, key := range []ContainerCgroupKey{
		{CgroupID: 101}, // shared by first and second
		{CSS: 301},      // shared by first and third
	} {
		if got := keys[key]; got != nil {
			t.Fatalf("duplicate key %+v = %s, want it dropped", key, got.ID)
		}
	}
	for key, want := range map[ContainerCgroupKey]string{
		{CSS: 302}:      "second",
		{CgroupID: 103}: "third", // third's CSS key was dropped, its ID key survives
	} {
		if got := keys[key]; got == nil || got.ID != want {
			t.Fatalf("key %+v = %+v, want container %s", key, got, want)
		}
	}
}

func TestContainerCgroupKeyBinarySize(t *testing.T) {
	if got, want := binary.Size(ContainerCgroupKey{}), 16; got != want {
		t.Fatalf("ContainerCgroupKey size = %d, want %d", got, want)
	}
}
