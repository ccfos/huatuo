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

//go:build integration

package bpf

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestGuestTraceFSMountIntegration(t *testing.T) {
	if os.Getenv("HUATUO_ISOLATED_MOUNT_TEST") != "1" {
		t.Skip("run in a private mount namespace with HUATUO_ISOLATED_MOUNT_TEST=1")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "kernel/tracing"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, name := range []string{"debug", "tracing"} {
			if err := unix.Unmount(filepath.Join(root, "kernel", name), 0); err != nil {
				t.Error(err)
			}
		}
	})
	if err := prepareTraceFS(root); err != nil {
		t.Fatal(err)
	}
	if err := prepareTraceFS(root); err != nil {
		t.Errorf("second mount preparation: %v", err)
	}
	for _, mount := range []struct {
		name  string
		magic int64
	}{{"tracing", unix.TRACEFS_MAGIC}, {"debug", unix.DEBUGFS_MAGIC}} {
		var stat unix.Statfs_t
		if err := unix.Statfs(filepath.Join(root, "kernel", mount.name), &stat); err != nil {
			t.Fatal(err)
		}
		if stat.Type != mount.magic {
			t.Errorf("%s filesystem type=%x, want %x", mount.name, stat.Type, mount.magic)
		}
	}
}

func TestGuestTraceFSLegacyLayoutIntegration(t *testing.T) {
	if os.Getenv("HUATUO_ISOLATED_MOUNT_TEST") != "1" {
		t.Skip("requires private mount namespace")
	}
	root := t.TempDir()
	debug := filepath.Join(root, "kernel/debug")
	if err := prepareTracingMount(debug, "debugfs", unix.DEBUGFS_MAGIC); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = unix.Unmount(filepath.Join(debug, "tracing"), 0)
		if err := unix.Unmount(debug, 0); err != nil {
			t.Error(err)
		}
	})
	if _, err := os.Stat(filepath.Join(debug, "tracing")); err != nil {
		t.Skip("kernel does not expose tracing under debugfs")
	}
	if err := prepareTraceFS(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "kernel/tracing")); !os.IsNotExist(err) {
		t.Error("legacy preparation created the unavailable sysfs mountpoint")
	}
}
