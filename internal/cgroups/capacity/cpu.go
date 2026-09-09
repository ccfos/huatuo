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

// Package capacity reads CPU ceilings without changing raw leaf quota APIs.
package capacity

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"huatuo-bamai/internal/cgroups/stats"
	"huatuo-bamai/internal/utils/cpuutil"
)

// ReadV1 uses the same relative path in the cpu, cpuacct and cpuset controllers.
func ReadV1(root, path, onlineCPUs string) (*stats.CpuCapacity, error) {
	return read(root, path, onlineCPUs, false)
}

// ReadV2 stops at the visible root; limits above that mount are not observable.
func ReadV2(root, path, onlineCPUs string) (*stats.CpuCapacity, error) {
	return read(root, path, onlineCPUs, true)
}

type reader struct {
	cores    float64
	config   strings.Builder
	cpuLists []string
}

type hierarchy struct {
	root *os.Root
	dirs []string
}

func read(root, path, onlineCPUs string, unified bool) (*stats.CpuCapacity, error) {
	count, err := cpuutil.ParseCPUListCount(onlineCPUs)
	if err != nil {
		return nil, fmt.Errorf("parse online CPUs: %w", err)
	}
	if count == 0 {
		return nil, errors.New("online CPU list is empty")
	}
	r := reader{cores: float64(count), cpuLists: []string{onlineCPUs}}
	r.record("online", strings.TrimSpace(onlineCPUs), path, strconv.FormatBool(unified))
	cpuRoot := root
	if !unified {
		cpuRoot = filepath.Join(root, "cpu")
	}
	cpu, err := r.openHierarchy(cpuRoot, path)
	if err != nil {
		return nil, err
	}
	defer cpu.root.Close()

	for _, dir := range cpu.dirs {
		if unified {
			err = r.quotaV2(cpu, dir)
		} else {
			err = r.quotaV1(cpu, dir)
		}
		if err != nil {
			return nil, err
		}
	}
	if unified {
		err = r.cpuset(cpu, true)
	} else {
		// cpuacct can be a separate hierarchy: its generation owns usage deltas.
		usage, usageErr := r.openHierarchy(filepath.Join(root, "cpuacct"), path)
		if usageErr != nil {
			return nil, usageErr
		}
		_ = usage.root.Close()
		cpusetRoot := filepath.Join(root, "cpuset")
		var cpuset *hierarchy
		cpuset, err = r.openHierarchy(cpusetRoot, path)
		if err != nil {
			// An absent controller is different from a missing container in it.
			if _, rootErr := os.Lstat(cpusetRoot); errors.Is(rootErr, fs.ErrNotExist) {
				r.record("cpuset controller absent", cpusetRoot)
				err = nil
			}
		} else {
			defer cpuset.root.Close()
			err = r.cpuset(cpuset, false)
		}
	}
	if err != nil {
		return nil, err
	}
	available, err := cpuutil.CPUListIntersectionCount(r.cpuLists...)
	if err != nil {
		return nil, fmt.Errorf("intersect online and cpuset CPUs: %w", err)
	}
	if available == 0 {
		return nil, errors.New("cpuset has no online CPUs")
	}
	r.cores = min(r.cores, float64(available))
	return &stats.CpuCapacity{
		Cores:    r.cores,
		ConfigID: sha256.Sum256([]byte(r.config.String())),
	}, nil
}

func (r *reader) record(parts ...string) {
	for _, part := range parts {
		r.config.WriteString(part)
		r.config.WriteByte(0)
	}
}

func (r *reader) openHierarchy(root, path string) (*hierarchy, error) {
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if part == ".." {
			return nil, fmt.Errorf("cgroup path %q contains a parent traversal", path)
		}
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve cgroup root %q: %w", root, err)
	}
	realRoot, err = filepath.Abs(realRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve absolute cgroup root %q: %w", realRoot, err)
	}
	leaf, err := filepath.EvalSymlinks(filepath.Join(realRoot, strings.TrimPrefix(path, "/")))
	if err != nil {
		return nil, fmt.Errorf("resolve cgroup %q in %q: %w", path, root, err)
	}
	relative, err := filepath.Rel(realRoot, leaf)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("cgroup path %q escapes root %q", path, root)
	}
	// Root also confines individual control-file symlinks during the read.
	directory, err := os.OpenRoot(realRoot)
	if err != nil {
		return nil, fmt.Errorf("open cgroup root %q: %w", realRoot, err)
	}
	h := &hierarchy{root: directory}
	r.record("hierarchy", root, realRoot)
	for dir := relative; ; dir = filepath.Dir(dir) {
		info, err := directory.Stat(dir)
		if err != nil || !info.IsDir() {
			_ = directory.Close()
			if err != nil {
				return nil, fmt.Errorf("stat cgroup %q in %q: %w", dir, realRoot, err)
			}
			return nil, fmt.Errorf("cgroup %q in %q is not a directory", dir, realRoot)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			_ = directory.Close()
			return nil, fmt.Errorf("cgroup %q in %q has no filesystem identity", dir, realRoot)
		}
		r.record(dir, strconv.FormatUint(stat.Dev, 10), strconv.FormatUint(stat.Ino, 10))
		h.dirs = append(h.dirs, dir)
		if dir == "." {
			return h, nil
		}
	}
}

func (r *reader) readFile(h *hierarchy, dir, name string) (string, error) {
	file := filepath.Join(dir, name)
	data, err := fs.ReadFile(h.root.FS(), file)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			r.record(file, "absent")
		}
		return "", fmt.Errorf("read CPU configuration %q in %q: %w", file, h.root.Name(), err)
	}
	value := strings.TrimSpace(string(data))
	r.record(file, value)
	return value, nil
}

func (r *reader) quotaV2(h *hierarchy, dir string) error {
	value, err := r.readFile(h, dir, "cpu.max")
	if err != nil {
		if dir == "." && errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	fields := strings.Fields(value)
	if len(fields) != 2 {
		return fmt.Errorf("invalid cpu.max %q at %q: expected quota and period", value, dir)
	}
	period, err := positive(fields[1], "cpu.max period", dir)
	if err != nil {
		return err
	}
	if fields[0] == "max" {
		return nil
	}
	quota, err := positive(fields[0], "cpu.max quota", dir)
	if err != nil {
		return err
	}
	r.cores = min(r.cores, float64(quota)/float64(period))
	return nil
}

func (r *reader) quotaV1(h *hierarchy, dir string) error {
	quota, err := r.readFile(h, dir, "cpu.cfs_quota_us")
	if err != nil {
		return err
	}
	periodText, err := r.readFile(h, dir, "cpu.cfs_period_us")
	if err != nil {
		return err
	}
	period, err := positive(periodText, "cpu.cfs_period_us", dir)
	if err != nil {
		return err
	}
	if quota == "-1" {
		return nil
	}
	value, err := positive(quota, "cpu.cfs_quota_us", dir)
	if err != nil {
		return err
	}
	if value > math.MaxInt64 {
		return fmt.Errorf("cpu.cfs_quota_us exceeds int64 at %q", dir)
	}
	r.cores = min(r.cores, float64(value)/float64(period))
	return nil
}

func positive(value, name, dir string) (uint64, error) {
	number, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q at %q: %w", name, value, dir, err)
	}
	if number == 0 {
		return 0, fmt.Errorf("%s is zero at %q", name, dir)
	}
	return number, nil
}

func (r *reader) cpuset(h *hierarchy, unified bool) error {
	found := false
	for _, dir := range h.dirs {
		name := "cpuset.cpus.effective"
		if !unified {
			name = "cpuset.effective_cpus"
		}
		value, err := r.readFile(h, dir, name)
		if errors.Is(err, fs.ErrNotExist) && !unified {
			name = "cpuset.cpus"
			value, err = r.readFile(h, dir, name)
		}
		if err != nil {
			if unified && errors.Is(err, fs.ErrNotExist) {
				// A controller not enabled here still constrains descendants
				// through the nearest ancestor where it is enabled.
				continue
			}
			return err
		}
		count, err := cpuutil.ParseCPUListCount(value)
		if err != nil {
			return fmt.Errorf("invalid %s at %q: %w", name, dir, err)
		}
		if unified && found {
			// The nearest effective set already includes kernel constraints.
			// Ancestor partitions can retain disjoint CPUs, or none at all.
			continue
		}
		if count == 0 {
			if name == "cpuset.cpus" && dir != "." {
				continue
			}
			return fmt.Errorf("%s is empty at %q", name, dir)
		}
		found = true
		r.cpuLists = append(r.cpuLists, value)
	}
	if !found {
		r.record("cpuset controller unavailable")
	}
	return nil
}
