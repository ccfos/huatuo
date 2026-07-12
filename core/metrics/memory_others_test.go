// Copyright 2026 The HuaTuo Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package collector

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"huatuo-bamai/internal/cgroups/paths"
	"huatuo-bamai/internal/cgroups/subsystem"
	"huatuo-bamai/pkg/tracing"
	"huatuo-bamai/pkg/types"
)

// setCgroupRootfs points the cgroup rootfs at a temp dir containing an empty
// memory subsystem directory, and restores the original on cleanup.
func setCgroupRootfs(t *testing.T) string {
	t.Helper()

	orig := paths.RootfsDefaultPath
	t.Cleanup(func() { paths.RootfsDefaultPath = orig })

	paths.RootfsDefaultPath = t.TempDir()

	memDir := filepath.Join(paths.RootfsDefaultPath, subsystem.SubsystemMemory)
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatalf("create memory cgroup dir: %v", err)
	}

	return memDir
}

func TestNewMemOthersCollectorNotSupported(t *testing.T) {
	setCgroupRootfs(t)

	// No Didi memcg extension files: the collector must opt out with
	// ErrNotSupported so the framework marks it inactive.
	if _, err := newMemOthersCollector(); !errors.Is(err, types.ErrNotSupported) {
		t.Fatalf("newMemOthersCollector() error = %v, want ErrNotSupported", err)
	}
}

func TestNewMemOthersCollectorSupported(t *testing.T) {
	memDir := setCgroupRootfs(t)

	stat := filepath.Join(memDir, "memory.directstall_stat")
	if err := os.WriteFile(stat, []byte("directstall_time 0\n"), 0o600); err != nil {
		t.Fatalf("create %s: %v", stat, err)
	}

	attr, err := newMemOthersCollector()
	if err != nil {
		t.Fatalf("newMemOthersCollector() error = %v, want nil", err)
	}
	if attr == nil || attr.Flag&tracing.FlagMetric == 0 {
		t.Fatalf("newMemOthersCollector() attr = %+v, want metric collector", attr)
	}
}
