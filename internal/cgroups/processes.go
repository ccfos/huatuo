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

package cgroups

import (
	"fmt"
	"path/filepath"

	"github.com/ccfos/huatuo/internal/cgroups/pids"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
)

// ProcsRecursive includes processes placed in descendant cgroups.
func ProcsRecursive(path string) ([]int32, error) {
	root := RootfsDefaultPath()
	switch mode := CgroupMode(); mode {
	case Legacy, Hybrid:
		root = RootFsFilePath(subsystem.SubsystemCPU)
	case Unified:
	default:
		return nil, fmt.Errorf("unsupported cgroup mode %d", mode)
	}
	return pids.Subtree(filepath.Join(root, path), "cgroup.procs")
}
