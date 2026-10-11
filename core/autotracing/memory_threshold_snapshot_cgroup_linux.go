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
	"fmt"
	"os"
	"path/filepath"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/stats"
)

// cgroupRef distinguishes directory instances when a path is reused.
type cgroupRef struct {
	Path      string
	directory os.FileInfo
}

func (r cgroupRef) SameInstance(other cgroupRef) bool {
	return r.Path == other.Path && r.directory != nil && other.directory != nil &&
		os.SameFile(r.directory, other.directory)
}

// Dependencies are immutable after construction and shared with the capture worker.
type cgroupSource struct {
	root   string
	cgroup cgroups.Cgroup
}

func newCgroupSource() (*cgroupSource, error) {
	root, err := cgroups.MemoryRoot()
	if err != nil {
		return nil, err
	}
	manager, err := cgroups.NewManager()
	if err != nil {
		return nil, fmt.Errorf("create cgroup manager: %w", err)
	}
	return &cgroupSource{root: root, cgroup: manager}, nil
}

func (s *cgroupSource) Validate(ctx context.Context, ref cgroupRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !filepath.IsAbs(ref.Path) || filepath.Clean(ref.Path) != ref.Path || ref.Path == "/" {
		return fmt.Errorf("invalid memory cgroup path %q", ref.Path)
	}

	info, err := os.Lstat(s.memcgDir(ref.Path))
	if err != nil {
		return err
	}
	if !info.IsDir() || !ref.SameInstance(cgroupRef{Path: ref.Path, directory: info}) {
		return fmt.Errorf("memory cgroup %q was replaced", ref.Path)
	}

	return ctx.Err()
}

func (s *cgroupSource) ReadMemory(ctx context.Context, ref cgroupRef) (stats.MemoryUsage, error) {
	if err := ctx.Err(); err != nil {
		return stats.MemoryUsage{}, err
	}
	usage, err := s.cgroup.MemoryUsage(ref.Path)
	if err != nil {
		return stats.MemoryUsage{}, fmt.Errorf("read cgroup memory usage: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return stats.MemoryUsage{}, err
	}
	if usage == nil {
		return stats.MemoryUsage{}, fmt.Errorf("memory usage unavailable for cgroup %q", ref.Path)
	}
	return *usage, nil
}

func (s *cgroupSource) memcgDir(path string) string { return filepath.Join(s.root, path) }
