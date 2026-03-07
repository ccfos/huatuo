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

package paths

import (
	"path/filepath"
	"testing"
)

func TestPath(t *testing.T) {
	cases := []struct {
		name     string
		segments []string
		want     string
	}{
		{
			name:     "no-segments-returns-rootfs-default",
			segments: []string{},
			want:     RootfsDefaultPath,
		},
		{
			name:     "single-segment-huatuo-dev",
			segments: []string{"huatuo-dev"},
			want:     filepath.Join(RootfsDefaultPath, "huatuo-dev"),
		},
		{
			name:     "two-segments-huatuo-region-memory",
			segments: []string{"huatuo-region", "memory"},
			want:     filepath.Join(RootfsDefaultPath, "huatuo-region", "memory"),
		},
		{
			name:     "nested-path-huatuo-dev-cpu-stat",
			segments: []string{"huatuo-dev", "cpu", "cpu.stat"},
			want:     filepath.Join(RootfsDefaultPath, "huatuo-dev", "cpu", "cpu.stat"),
		},
		{
			// filepath.Join cleans ".." — the result must not escape rootfs root.
			name:     "dotdot-segment-is-cleaned",
			segments: []string{"huatuo-dev", "..", "huatuo-region"},
			want:     filepath.Join(RootfsDefaultPath, "huatuo-dev", "..", "huatuo-region"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Path(tc.segments...); got != tc.want {
				t.Errorf("Path(%v): got %q, want %q", tc.segments, got, tc.want)
			}
		})
	}
}

// TestRootfsDefaultPath verifies the package-level default points to the
// standard cgroup v2 mount location expected by the HuaTuo runtime.
func TestRootfsDefaultPath(t *testing.T) {
	const wantCgroupV2Root = "/sys/fs/cgroup"
	if RootfsDefaultPath != wantCgroupV2Root {
		t.Errorf("RootfsDefaultPath: got %q, want %q", RootfsDefaultPath, wantCgroupV2Root)
	}
}
