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

package pod

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
)

// parseCgroupCPUCSS resolves the VM controller independently of truncated names
// or the sandbox ID, which can also occur in a separate overhead cgroup.
func parseCgroupCPUCSS(cgroupPath string) (map[string]uint64, error) {
	root := cgroups.RootFsFilePath(subsystem.SubsystemCPU)
	notify := cgroupv1NotifyFile
	if cgroups.CgroupMode() == cgroups.Unified {
		root = cgroups.RootfsDefaultPath()
		notify = cgroupv2CPUNotifyFile
	}
	var stat syscall.Stat_t
	if err := syscall.Stat(filepath.Join(root, cgroupPath), &stat); err != nil {
		return nil, fmt.Errorf("stat CPU cgroup %q: %w", cgroupPath, err)
	}
	// Discovery may run before the controller was enabled. The optional probe
	// refreshes the cache; its absence must not discard lifecycle-cached data.
	_, _ = os.ReadFile(filepath.Join(root, cgroupPath, notify))
	var addr uint64
	var conflict bool
	cgroupCssMetaDataMap.Range(func(_, value any) bool {
		m := value.(*containerCssMetaData)
		if m.Inode == stat.Ino && m.SubSys == subsystem.SubsystemCPU && m.CSS != 0 {
			if addr != 0 && addr != m.CSS {
				conflict = true
				return false
			}
			addr = m.CSS
		}
		return true
	})
	if conflict || addr == 0 {
		return nil, fmt.Errorf("CPU CSS unavailable or ambiguous for cgroup %q (inode %d)", cgroupPath, stat.Ino)
	}
	return map[string]uint64{subsystem.SubsystemCPU: addr}, nil
}
