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
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	promprocfs "github.com/prometheus/procfs"

	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/procfs"
)

// Directory returns a validated controller directory, never the host root.
// Rechecking existence also stops file collection while discovery is unavailable.
func (b *CgroupBinding) Directory(controller string) (string, error) {
	directory := b.directoryFor(controller)
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

func (b *CgroupBinding) directoryFor(controller string) string {
	if directory, exists := b.directories[controller]; exists {
		return directory
	}
	return b.directories[""]
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
	return cgroupMountsAt(procfs.DefaultPath())
}

func cgroupMountsAt(root string) ([]*promprocfs.MountInfo, error) {
	fs, err := procfs.NewFS(root)
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
			delete(binding.kernelGroups, metadata.SubSys)
			conflicts[metadata.SubSys] = true
		} else {
			binding.CSS[metadata.SubSys] = metadata.CSS
			if metadata.Cgroup != 0 {
				if binding.kernelGroups == nil {
					binding.kernelGroups = make(map[string]uint64)
				}
				binding.kernelGroups[metadata.SubSys] = metadata.Cgroup
			}
		}
		return true
	})
	return binding
}

func (v *containerStore) indexCgroups(record *containerRecord, add bool) {
	if record.pod == nil {
		return
	}
	binding := &record.pod.Cgroups
	for controller, css := range binding.CSS {
		if css == 0 || binding.directoryFor(controller) == "" {
			continue
		}
		// CSS and cgroup pointers use separate indexes even if their numeric
		// values coincide. Only locally verified bindings enter these indexes.
		for _, resource := range [...]struct {
			address uint64
			index   map[uint64]map[string]struct{}
		}{{css, v.cssOwners}, {binding.kernelGroups[controller], v.cgroupOwners}} {
			if resource.address == 0 {
				continue
			}
			owners := resource.index[resource.address]
			if add {
				if owners == nil {
					owners = make(map[string]struct{})
					resource.index[resource.address] = owners
				}
				owners[record.ref.Key.ID] = struct{}{}
			} else {
				delete(owners, record.ref.Key.ID)
				if len(owners) == 0 {
					delete(resource.index, resource.address)
				}
			}
		}
	}
}

// cgroupEvent returns a targeted hint for one known owner. Unknown or shared
// resources use a coalesced full refresh; kernel events never remove membership.
func (v *containerStore) cgroupEvent(event *containerCssPerfEvent) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	var hint string
	multiple, invalidated := false, false
	visit := func(owners map[string]struct{}) {
		for id := range owners {
			if hint != "" && hint != id {
				multiple = true
			}
			hint = id
			if event.Operation == abi.CgroupCSSOperationRemove {
				record := v.records[id]
				if record == nil {
					continue
				}
				updated := *record
				updated.pod = record.pod.copyMetadata()
				for controller, css := range record.pod.Cgroups.CSS {
					if (event.Cgroup != 0 && record.pod.Cgroups.kernelGroups[controller] == event.Cgroup) ||
						(css != 0 && slices.Contains(event.CSS[:], css)) {
						updated.pod.Cgroups.invalidateDirectory(record.pod.Cgroups.directoryFor(controller))
					}
				}
				if record.container != nil {
					legacy := *record.container
					legacy.CgroupCss = maps.Clone(legacy.CgroupCss)
					for controller, css := range record.pod.Cgroups.CSS {
						if legacy.CgroupCss[controller] == css && updated.pod.Cgroups.CSS[controller] != css {
							delete(legacy.CgroupCss, controller)
						}
					}
					updated.container = &legacy
				}
				v.indexCgroups(record, false)
				v.records[id] = &updated
				v.indexCgroups(&updated, true)
				invalidated = true
			}
		}
	}
	if owners := v.cgroupOwners[event.Cgroup]; len(owners) != 0 {
		visit(owners)
	} else {
		for _, css := range event.CSS {
			if css != 0 {
				visit(v.cssOwners[css])
			}
		}
	}
	if invalidated {
		v.revision++
		for subscriber := range v.subscribers {
			subscriber.needsFull = true
			subscriber.signal()
		}
	}
	if multiple {
		return ""
	}
	return hint
}

func (b *CgroupBinding) invalidateDirectory(directory string) {
	if directory == "" {
		return
	}
	for controller := range b.CSS {
		if b.directoryFor(controller) == directory {
			delete(b.CSS, controller)
			delete(b.kernelGroups, controller)
		}
	}
	for controller, bound := range b.directories {
		if bound == directory {
			// Keep an explicit unavailable entry to mask unified fallback.
			b.directories[controller] = ""
			delete(b.Controllers, controller)
			if controller == "" {
				b.Unified = ""
			}
		}
	}
}
