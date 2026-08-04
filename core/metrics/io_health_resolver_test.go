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

package collector

import (
	"os"
	"path/filepath"
	"testing"

	"huatuo-bamai/internal/ioobserve/health"
)

func TestIOHealthResolverNormalizesRecursivePartitionLeaf(t *testing.T) {
	root := t.TempDir()
	physical := filepath.Join(root, "devices", "block", "sda")
	partition := filepath.Join(physical, "sda1")
	if err := os.MkdirAll(filepath.Join(physical, "slaves"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(partition, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partition, "partition"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	classBlock := filepath.Join(root, "class", "block")
	if err := os.MkdirAll(filepath.Join(classBlock, "dm-0", "slaves"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(physical, filepath.Join(classBlock, "sda")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(partition, filepath.Join(classBlock, "sda1")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		partition,
		filepath.Join(classBlock, "dm-0", "slaves", "sda1"),
	); err != nil {
		t.Fatal(err)
	}

	target := newIOHealthResolver(root).resolveBlockName("dm-0")
	if target.eventDevice != "dm-0" ||
		target.target != "sda" ||
		target.protocol != ioHealthProtocolSCSI ||
		target.reason != "" {
		t.Fatalf("resolved target = %+v", target)
	}
}

func TestIOHealthResolverRejectsMultipleLeaves(t *testing.T) {
	root := t.TempDir()
	classBlock := filepath.Join(root, "class", "block")
	for _, device := range []string{"sda", "sdb"} {
		if err := os.MkdirAll(
			filepath.Join(classBlock, device, "slaves"),
			0o755,
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(classBlock, "dm-0", "slaves"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, device := range []string{"sda", "sdb"} {
		if err := os.Symlink(
			filepath.Join(classBlock, device),
			filepath.Join(classBlock, "dm-0", "slaves", device),
		); err != nil {
			t.Fatal(err)
		}
	}

	target := newIOHealthResolver(root).resolveBlockName("dm-0")
	if target.reason != health.CollectionReasonTargetUnsupported ||
		target.target != "" {
		t.Fatalf("resolved target = %+v", target)
	}
}

func TestIOHealthResolverUsesExplicitNVMeControllerPath(t *testing.T) {
	root := t.TempDir()
	classBlock := filepath.Join(root, "class", "block")
	if err := os.MkdirAll(
		filepath.Join(classBlock, "nvme0c1n1", "slaves"),
		0o755,
	); err != nil {
		t.Fatal(err)
	}

	target := newIOHealthResolver(root).resolveBlockName("nvme0c1n1")
	if target.target != "nvme1" ||
		target.protocol != ioHealthProtocolNVMe ||
		target.reason != "" {
		t.Fatalf("resolved target = %+v", target)
	}
}

func TestIOHealthResolverRejectsUnattributedNVMeMultipathHead(t *testing.T) {
	root := t.TempDir()
	namespace := filepath.Join(
		root,
		"devices",
		"virtual",
		"nvme-subsystem",
		"nvme-subsys0",
		"nvme0n1",
	)
	if err := os.MkdirAll(namespace, 0o755); err != nil {
		t.Fatal(err)
	}
	classNamespace := filepath.Join(root, "class", "block", "nvme0n1")
	if err := os.MkdirAll(filepath.Join(classNamespace, "slaves"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(namespace, filepath.Join(classNamespace, "device")); err != nil {
		t.Fatal(err)
	}

	target := newIOHealthResolver(root).resolveBlockName("nvme0n1")
	if target.reason != health.CollectionReasonTargetUnsupported || target.target != "" {
		t.Fatalf("resolved target = %+v", target)
	}
}

func TestIOHealthResolverTreatsMissingDeviceAsUnresolved(t *testing.T) {
	target := newIOHealthResolver(t.TempDir()).resolveBlockName("gone")
	if target.reason != health.CollectionReasonTargetUnresolved {
		t.Fatalf("resolved target = %+v", target)
	}
}
