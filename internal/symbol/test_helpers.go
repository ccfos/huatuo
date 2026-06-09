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

package symbol

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"huatuo-bamai/internal/procfs"
)

func setupTempProcRoot(t *testing.T) string {
	t.Helper()
	tmpRoot := t.TempDir()
	originalPrefix := filepath.Dir(procfs.DefaultPath())
	procfs.RootPrefix(tmpRoot)
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })
	return tmpRoot
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", path, err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink(%q -> %q): %v", link, target, err)
	}
}

func bytesFramesToStrings(frames [][]byte) []string {
	result := make([]string, 0, len(frames))
	for _, frame := range frames {
		result = append(result, string(frame))
	}
	return result
}

func setTestXfsMounts(t *testing.T, xfsMounts []string) {
	t.Helper()
	originalMounts := mounts
	originalInited := mountsInited

	mounts = append([]string{}, xfsMounts...)
	mountsInited = true

	t.Cleanup(func() {
		mounts = originalMounts
		mountsInited = originalInited
	})
}

// setupHostProcessProcFS makes utils.IsProcessInContainer(pid) return false by
// pointing /proc/1/ns/mnt and /proc/<pid>/ns/mnt at the same namespace id and
// writing a non-container /proc/<pid>/cgroup.
func setupHostProcessProcFS(t *testing.T, tmpRoot string, pid uint32) {
	t.Helper()
	const sharedMountNS = "mnt:[4026531840]"

	hostNSDir := filepath.Join(tmpRoot, "proc", "1", "ns")
	pidDir := filepath.Join(tmpRoot, "proc", strconv.Itoa(int(pid)))
	pidNSDir := filepath.Join(pidDir, "ns")
	mustMkdirAll(t, hostNSDir)
	mustMkdirAll(t, pidNSDir)
	mustSymlink(t, sharedMountNS, filepath.Join(hostNSDir, "mnt"))
	mustSymlink(t, sharedMountNS, filepath.Join(pidNSDir, "mnt"))
	mustWriteFile(t, filepath.Join(pidDir, "cgroup"), "0::/user.slice/user-1000.slice\n")
}
