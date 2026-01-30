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

package procfs

import (
	"path/filepath"

	"github.com/prometheus/procfs"
)

var (
	// defaultProcMountPoint is the common mount point of the proc filesystem.
	defaultProcMountPoint = "/proc"
	// defaultSysMountPoint is the common mount point of the sys filesystem.
	defaultSysMountPoint = "/sys"
	// defaultDevMountPoint is the common mount point of the dev path.
	defaultDevMountPoint = "/dev"
)

// defaultPaths defines resolvers for default mount points of /proc, /sys, and /dev.
// functions are used to allow runtime overrides (e.g. via RootPrefix in tests).
var defaultPaths = map[string]func() string{
	"proc": func() string { return defaultProcMountPoint },
	"sys":  func() string { return defaultSysMountPoint },
	"dev":  func() string { return defaultDevMountPoint },
}

type FS = procfs.FS

// RootPrefix add prefix for /proc, /sys, and /dev. Invoked only for integration test.
func RootPrefix(root string) {
	if root == "" {
		return
	}

	defaultProcMountPoint = filepath.Join(root, defaultProcMountPoint)
	defaultSysMountPoint = filepath.Join(root, defaultSysMountPoint)
	defaultDevMountPoint = filepath.Join(root, defaultDevMountPoint)
}

// NewDefaultFS returns a new proc FS using runtime-initialized mount points.
func NewDefaultFS() (FS, error) {
	return procfs.NewFS(defaultProcMountPoint)
}

// NewFS returns a new proc FS mounted under the given proc mountPoint. It will error
// if the mount point directory can't be read or is a file.
func NewFS(mountPoint string) (FS, error) {
	return procfs.NewFS(mountPoint)
}

// DefaultPath returns the default proc path, e.g. "/proc".
func DefaultPath() string {
	return defaultProcMountPoint
}

// Path returns a new path with default prefix, e.g. "/proc/[p]".
func Path(p ...string) string {
	fs := defaultProcMountPoint

	return filepath.Join(append([]string{fs}, p...)...)
}

// DefaultPathByType returns the default path with types.
func DefaultPathByType(pathType string) string {
	if fn, ok := defaultPaths[pathType]; ok {
		return fn()
	}

	return ""
}
