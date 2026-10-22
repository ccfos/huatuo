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

package autotracing

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/cgroups/paths"
	"github.com/ccfos/huatuo/internal/pod"
)

func cgroupRefForTest(t testing.TB, source *cgroupSource, path string) cgroupRef {
	t.Helper()
	info, err := os.Lstat(source.memcgDir(path))
	if err != nil {
		t.Fatal(err)
	}
	return cgroupRef{Path: path, directory: info}
}

// Directory lookup stands in for pod's immutable published bindings.
func fixtureCgroupTracker(source *cgroupSource, watcher *memoryThresholdWatcher) *cgroupTracker {
	tracker := newCgroupTracker(watcher)
	tracker.directory = func(ref pod.ContainerRef) (os.FileInfo, error) {
		return os.Lstat(source.memcgDir(ref.MemoryCgroupPath))
	}
	return tracker
}

func TestMemoryCgroupSourceDetectsDirectoryReplacement(t *testing.T) {
	source := newTestCgroupSource(t)
	path := "/" + strings.Repeat("a", 64)
	createMemoryCgroupForTest(t, source.root, path, 95)
	target := cgroupRefForTest(t, source, path)
	if err := source.Validate(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(source.memcgDir(path), filepath.Join(source.root, "old-instance")); err != nil {
		t.Fatal(err)
	}
	createMemoryCgroupForTest(t, source.root, path, 95)
	if err := source.Validate(t.Context(), target); err == nil {
		t.Fatal("same-path replacement passed instance validation")
	}
}

func TestMemoryCgroupSourceValidatesReferences(t *testing.T) {
	source := newTestCgroupSource(t)
	path := "/workloads/application"
	createMemoryCgroupForTest(t, source.root, path, 95)
	ref := cgroupRefForTest(t, source, path)
	if err := source.Validate(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", "relative", "/", "/workloads/../application"} {
		ref.Path = path
		if err := source.Validate(t.Context(), ref); err == nil {
			t.Fatalf("accepted path %q", path)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := source.Validate(ctx, ref); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled validation: %v", err)
	}
}

func containerRefForTest(path string) pod.ContainerRef {
	return pod.ContainerRef{Key: pod.ContainerKey{ID: filepath.Base(path), Generation: 1}, InitPID: 42, MemoryCgroupPath: path}
}

func writeMemoryEventsForTest(t testing.TB, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newTestCgroupSource(t testing.TB) *cgroupSource {
	t.Helper()
	previous := paths.RootfsDefaultPath
	paths.RootfsDefaultPath = t.TempDir()
	t.Cleanup(func() { paths.RootfsDefaultPath = previous })
	if err := os.MkdirAll(filepath.Join(paths.RootfsDefaultPath, "memory"), 0o700); err != nil {
		t.Fatal(err)
	}
	source, err := newCgroupSource()
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func createMemoryCgroupForTest(t testing.TB, root, path string, usage uint64) {
	t.Helper()
	directory := filepath.Join(root, path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"memory.current": fmt.Sprint(usage), "memory.max": "100", "memory.high": "max",
		"memory.usage_in_bytes": fmt.Sprint(usage), "memory.limit_in_bytes": "100",
		"cgroup.event_control": "", "memory.events": "high 0\nmax 0\n",
	} {
		writeMemoryEventsForTest(t, filepath.Join(directory, name), value)
	}
}
