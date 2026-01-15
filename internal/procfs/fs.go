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
	"github.com/prometheus/procfs"
)

var (
	// DefaultProcMountPoint is the common mount point of the proc filesystem.
	DefaultProcMountPoint = "/proc"

	// DefaultSysMountPoint is the common mount point of the sys filesystem.
	DefaultSysMountPoint = "/sys"
)

type FS = procfs.FS

// NewDefaultFS returns a new proc FS using runtime-initialized mount points.
func NewDefaultFS() (FS, error) {
	return procfs.NewFS(DefaultProcMountPoint)
}

// NewFS returns a new proc FS mounted under the given proc mount point.
// If procMount is empty, runtime default is used.
func NewFS(procMount string) (FS, error) {
	if procMount == "" {
		return NewDefaultFS()
	}
	return procfs.NewFS(DefaultProcMountPoint)
}
