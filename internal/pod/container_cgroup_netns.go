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
	"huatuo-bamai/internal/cgroups/subsystem"
	"huatuo-bamai/internal/log"
)

// ContainerCgroupNetNamespace contains the identifiers used to resolve an event's container.
// The fields are ordered by lookup priority.
type ContainerCgroupNetNamespace struct {
	MemoryCgroupCSSAddr uint64
	NetNamespaceCookie  uint64
	NetNamespaceInum    uint64
}

// ContainerIDByCgroupNetNamespace finds a container ID from cgroup and network namespace values.
// It prefers the memory cgroup CSS address, then the network namespace cookie, and finally the network namespace inum.
func ContainerIDByCgroupNetNamespace(ids ContainerCgroupNetNamespace) string {
	if ids.MemoryCgroupCSSAddr != 0 {
		container, err := ContainerByCSS(ids.MemoryCgroupCSSAddr, subsystem.SubsystemMemory)
		if err != nil {
			log.Debugf("container cgroup/netns: CSS lookup 0x%x: %v", ids.MemoryCgroupCSSAddr, err)
		} else if container != nil {
			return container.ID
		}
	}

	if ids.NetNamespaceCookie != 0 {
		container, err := ContainerByNetNamespaceCookie(ids.NetNamespaceCookie)
		if err != nil {
			log.Debugf("container cgroup/netns: netns_cookie lookup %d: %v", ids.NetNamespaceCookie, err)
		} else if container != nil {
			return container.ID
		}
	}

	if ids.NetNamespaceInum != 0 {
		container, err := ContainerByNetNamespaceInum(ids.NetNamespaceInum)
		if err != nil {
			log.Debugf("container cgroup/netns: netns_inum lookup %d: %v", ids.NetNamespaceInum, err)
		} else if container != nil {
			return container.ID
		}
	}

	return ""
}
