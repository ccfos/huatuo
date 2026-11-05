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
	"maps"
	"time"

	"github.com/ccfos/huatuo/internal/cgroups"
)

const (
	TypeContainer   = "container"
	TypeCubeSandbox = "cubesandbox"
	TypeOpenSandbox = "opensandbox"
)

// Pod is one monitored workload, independent of its discovery source.
// Published metadata is read-only; collectors synchronize their own life resources.
type Pod struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Name      string         `json:"name"`
	Cgroups   CgroupBinding  `json:"cgroups"`
	Process   *HostProcess   `json:"process,omitempty"`
	Network   *HostNetwork   `json:"network,omitempty"`
	StartedAt time.Time      `json:"started_at"`
	SyncedAt  time.Time      `json:"synced_at"`
	Labels    map[string]any `json:"labels"`
	Container *ContainerInfo `json:"container,omitempty"`

	lifeResources map[string]any
}

// CgroupBinding keeps membership paths separate from kernel CSS addresses.
// Paths are relative to controller mounts, including their leading slash.
type CgroupBinding struct {
	Unified     string            `json:"unified,omitempty"`
	Controllers map[string]string `json:"controllers,omitempty"`
	CSS         map[string]uint64 `json:"css,omitempty"`

	directories map[string]string
}

type HostProcess struct {
	PID int `json:"pid"`
}

// HostNetwork may carry IP metadata without a usable namespace binding.
type HostNetwork struct {
	PID             int    `json:"pid,omitempty"`
	NamespaceInum   uint64 `json:"namespace_inum,omitempty"`
	NamespaceCookie uint64 `json:"namespace_cookie,omitempty"`
	IPAddress       string `json:"ip_address,omitempty"`
}

// ContainerInfo preserves container-specific classification and runtime metadata.
type ContainerInfo struct {
	Hostname    string        `json:"hostname"`
	Type        ContainerType `json:"type"`
	Qos         ContainerQos  `json:"qos"`
	RuntimeType string        `json:"runtime_type,omitempty"`
	SandboxID   string        `json:"sandbox_id,omitempty"`
}

// PodByID returns the last published view without discovery or runtime I/O.
func PodByID(id string) (*Pod, error) {
	containerView.mu.RLock()
	defer containerView.mu.RUnlock()
	if record := containerView.records[id]; record != nil {
		return record.pod, nil
	}
	return nil, nil
}

// Pods keeps cached metadata available during transient discovery failures.
func Pods() (map[string]*Pod, error) {
	containerView.mu.RLock()
	defer containerView.mu.RUnlock()
	result := make(map[string]*Pod, len(containerView.records))
	for id, record := range containerView.records {
		if record.pod != nil {
			result[id] = record.pod
		}
	}
	return result, nil
}

func (p *Pod) LifeResources(key string) any {
	return p.lifeResources[key]
}

// podFromContainer is a migration boundary for the existing discovery producer.
// It preserves the legacy shared path until discovery provides controller paths.
func podFromContainer(c *Container) *Pod {
	p := &Pod{
		ID: c.ID, Type: TypeContainer, Name: c.Name,
		StartedAt: c.StartedAt, Labels: c.Labels,
		Cgroups: CgroupBinding{CSS: c.CgroupCss},
		Container: &ContainerInfo{
			Hostname: c.Hostname, Type: c.Type, Qos: c.Qos,
			RuntimeType: c.RuntimeType, SandboxID: c.SandboxID,
		},
	}
	if cgroups.CgroupMode() == cgroups.Unified {
		p.Cgroups.Unified = c.CgroupPath
		if memory := c.MemoryCgroupPath(); memory != c.CgroupPath {
			p.Cgroups.Controllers = map[string]string{"memory": memory}
		}
	} else {
		p.Cgroups.Controllers = map[string]string{
			"cpu": c.CgroupPath, "cpuacct": c.CgroupPath, "cpuset": c.CgroupPath,
			"blkio": c.CgroupPath, "memory": c.MemoryCgroupPath(),
		}
	}
	if c.InitPid > 0 {
		p.Process = &HostProcess{PID: c.InitPid}
	}
	if c.NetNamespaceInum != 0 || c.NetNamespaceCookie != 0 || c.IPAddress != "" {
		p.Network = &HostNetwork{
			NamespaceInum: c.NetNamespaceInum, NamespaceCookie: c.NetNamespaceCookie,
			IPAddress: c.IPAddress,
		}
		if c.NetNamespaceInum != 0 || c.NetNamespaceCookie != 0 {
			p.Network.PID = c.InitPid
		}
	}
	return p
}

// copyMetadata detaches the mutable containers of a discovery result. Label
// values are source-owned immutable metadata, not collector state.
func (p *Pod) copyMetadata() *Pod {
	view := *p
	view.Labels = maps.Clone(p.Labels)
	view.Cgroups.Controllers = maps.Clone(p.Cgroups.Controllers)
	view.Cgroups.CSS = maps.Clone(p.Cgroups.CSS)
	view.Cgroups.directories = maps.Clone(p.Cgroups.directories)
	if p.Process != nil {
		process := *p.Process
		view.Process = &process
	}
	if p.Network != nil {
		network := *p.Network
		view.Network = &network
	}
	if p.Container != nil {
		container := *p.Container
		view.Container = &container
	}
	return &view
}
