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

package sysfs

import (
	"path/filepath"

	"github.com/ccfos/huatuo/internal/procfs"

	"github.com/prometheus/procfs/sysfs"
)

type (
	FS       = sysfs.FS
	NetClass = sysfs.NetClass
)

// NewDefaultFS returns a new proc FS using runtime-initialized mount points.
func NewDefaultFS() (FS, error) {
	return sysfs.NewFS(DefaultPath())
}

// DefaultPath returns the default proc path, e.g. "/sys".
func DefaultPath() string {
	return procfs.DefaultPathByType("sys")
}

// Path returns a new path with default prefix, e.g. "/sys/[p].
func Path(p ...string) string {
	fs := DefaultPath()

	return filepath.Join(append([]string{fs}, p...)...)
}
