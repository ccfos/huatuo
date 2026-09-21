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
	"math"
	"path/filepath"

	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
)

// MemoryRoot resolves the memory hierarchy mount without exposing its version.
func MemoryRoot() (string, error) {
	root := RootfsDefaultPath()
	switch mode := CgroupMode(); mode {
	case Legacy, Hybrid:
		root = RootFsFilePath(subsystem.SubsystemMemory)
	case Unified:
	default:
		return "", fmt.Errorf("unsupported cgroup mode %d", mode)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve memory cgroup root %q: %w", root, err)
	}
	return resolved, nil
}

// IsMemoryLimitUnlimited recognizes the v1 and v2 unlimited representations.
func IsMemoryLimitUnlimited(limit uint64) bool {
	return limit == 0 || limit == math.MaxUint64 ||
		limit >= uint64(math.MaxInt64)-(1<<20)
}
