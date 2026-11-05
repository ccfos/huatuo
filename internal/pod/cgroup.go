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
	"strings"
	"syscall"

	promprocfs "github.com/prometheus/procfs"

	"github.com/ccfos/huatuo/internal/procfs"
)

// Directory returns a validated controller directory, never the host root.
// Rechecking existence also stops file collection while discovery is unavailable.
func (b *CgroupBinding) Directory(controller string) (string, error) {
	directory, exists := b.directories[controller]
	if !exists {
		directory = b.directories[""]
	}
	if directory == "" {
		return "", fmt.Errorf("cgroup controller %q is not bound", controller)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return "", fmt.Errorf("inspect %s cgroup %q: %w", controller, directory, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s cgroup %q is not a directory", controller, directory)
	}
	return directory, nil
}

// Path returns membership only while the corresponding directory is usable.
func (b *CgroupBinding) Path(controller string) (string, bool) {
	if _, err := b.Directory(controller); err != nil {
		return "", false
	}
	if value, exists := b.Controllers[controller]; exists {
		return value, true
	}
	return b.Unified, b.Unified != ""
}

// ProcessDirectory follows the kernel membership selection used by ProcessPaths.
func (b *CgroupBinding) ProcessDirectory() (string, error) {
	for _, controller := range []string{"", "cpu", "cpuacct", "pids"} {
		if directory, err := b.Directory(controller); err == nil {
			return directory, nil
		}
	}
	return "", fmt.Errorf("no bound cgroup directory for process enumeration")
}

func cgroupMounts() ([]*promprocfs.MountInfo, error) {
	fs, err := procfs.NewDefaultFS()
	if err != nil {
		return nil, err
	}
	return fs.GetMounts()
}

var mountPathUnescape = strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)

// resolveCgroupDirectory respects bind-mounted subtrees and combined v1 mounts.
// A controller's explicit v1 membership must not fall back to a v2 hierarchy.
func resolveCgroupDirectory(mounts []*promprocfs.MountInfo, controller, membership string) (string, os.FileInfo, error) {
	if !filepath.IsAbs(membership) || filepath.Clean(membership) != membership {
		return "", nil, fmt.Errorf("invalid %s cgroup membership %q", controller, membership)
	}
	var directory string
	var info os.FileInfo
	var rootLength int
	for _, mount := range mounts {
		if controller == "" {
			if mount.FSType != "cgroup2" {
				continue
			}
		} else if _, present := mount.SuperOptions[controller]; mount.FSType != "cgroup" || !present {
			continue
		}
		root := mountPathUnescape.Replace(mount.Root)
		relative, err := filepath.Rel(root, membership)
		if err != nil || relative == ".." || strings.HasPrefix(relative, "../") {
			continue
		}
		candidate := filepath.Join(mountPathUnescape.Replace(mount.MountPoint), relative)
		stat, err := os.Lstat(candidate)
		if err != nil || !stat.IsDir() || len(root) < rootLength {
			continue
		}
		directory, info, rootLength = candidate, stat, len(root)
	}
	if info == nil {
		return "", nil, fmt.Errorf("no mounted %s cgroup directory for %q", controller, membership)
	}
	return directory, info, nil
}

// bindCgroups publishes only verified locations. Callers retain locations for
// retries; CSS availability is independent of basic cgroup file availability.
func bindCgroups(locations CgroupBinding, mounts []*promprocfs.MountInfo) CgroupBinding {
	binding := CgroupBinding{directories: make(map[string]string)}
	inodes := make(map[string]uint64)
	bind := func(controller, membership string) bool {
		if membership == "" || membership == "/" {
			return false
		}
		directory, info, err := resolveCgroupDirectory(mounts, controller, membership)
		if err != nil {
			return false
		}
		binding.directories[controller] = directory
		inodes[controller] = info.Sys().(*syscall.Stat_t).Ino
		// These reads trigger existing kernel CSS probes when supported.
		if controller == "" {
			_, _ = os.ReadFile(filepath.Join(directory, cgroupv2NotifyFile))
			_, _ = os.ReadFile(filepath.Join(directory, cgroupv2CPUNotifyFile))
		} else {
			_, _ = os.ReadFile(filepath.Join(directory, cgroupv1NotifyFile))
		}
		return true
	}
	if bind("", locations.Unified) {
		binding.Unified = locations.Unified
	}
	for controller, membership := range locations.Controllers {
		if controller == "" {
			continue
		}
		// An unresolved v1 controller must mask the unified fallback on hybrid hosts.
		binding.directories[controller] = ""
		if bind(controller, membership) {
			if binding.Controllers == nil {
				binding.Controllers = make(map[string]string)
			}
			binding.Controllers[controller] = membership
		}
	}
	conflicts := make(map[string]bool)
	cgroupCssMetaDataMap.Range(func(_, value any) bool {
		metadata := value.(*containerCssMetaData)
		inode, explicit := inodes[metadata.SubSys]
		if !explicit {
			if _, masked := locations.Controllers[metadata.SubSys]; masked {
				return true
			}
			inode = inodes[""]
		}
		if inode == 0 || inode != metadata.Inode || metadata.CSS == 0 || conflicts[metadata.SubSys] {
			return true
		}
		if binding.CSS == nil {
			binding.CSS = make(map[string]uint64)
		}
		if previous := binding.CSS[metadata.SubSys]; previous != 0 && previous != metadata.CSS {
			delete(binding.CSS, metadata.SubSys)
			conflicts[metadata.SubSys] = true
		} else {
			binding.CSS[metadata.SubSys] = metadata.CSS
		}
		return true
	})
	return binding
}
