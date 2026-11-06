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
	"strconv"
	"syscall"
	"time"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/utils/netutil"
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

	directories  map[string]string
	kernelGroups map[string]uint64
}

type HostProcess struct {
	PID int `json:"pid"`

	startTime uint64
	procRoot  string
}

// HostNetwork may carry IP metadata without a usable namespace binding.
type HostNetwork struct {
	PID             int    `json:"pid,omitempty"`
	NamespaceInum   uint64 `json:"namespace_inum,omitempty"`
	NamespaceCookie uint64 `json:"namespace_cookie,omitempty"`
	IPAddress       string `json:"ip_address,omitempty"`

	startTime uint64
	procRoot  string
}

// ContainerInfo preserves container-specific classification and runtime metadata.
type ContainerInfo struct {
	Hostname    string        `json:"hostname"`
	Type        ContainerType `json:"type"`
	Qos         ContainerQos  `json:"qos"`
	RuntimeType string        `json:"runtime_type,omitempty"`
	SandboxID   string        `json:"sandbox_id,omitempty"`

	// CgroupPath preserves the legacy shared path independently of resource bindings.
	CgroupPath string `json:"-"`
	// Guest preserves local scanning's timestamps and optional memory hierarchy.
	Guest            bool   `json:"-"`
	MemoryCgroupPath string `json:"-"`
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

// ProcessPID returns only the still-current representative process.
func (p *Pod) ProcessPID() (int, error) {
	if p.Process == nil || p.Process.startTime == 0 {
		return 0, fmt.Errorf("workload %q has no bound process", p.ID)
	}
	if err := validateProcess(p.Process.PID, p.Process.startTime, p.Process.procRoot); err != nil {
		return 0, err
	}
	return p.Process.PID, nil
}

// NetworkPID verifies the process and namespace without assuming they match
// the workload's representative process or its advertised IP address.
func (p *Pod) NetworkPID() (int, error) {
	if p.Network == nil || p.Network.startTime == 0 || p.Network.NamespaceInum == 0 {
		return 0, fmt.Errorf("workload %q has no bound network namespace", p.ID)
	}
	if err := validateProcess(p.Network.PID, p.Network.startTime, p.Network.procRoot); err != nil {
		return 0, err
	}
	inode, err := networkInode(p.Network.PID, p.Network.procRoot)
	if err != nil {
		return 0, err
	}
	if inode != p.Network.NamespaceInum {
		return 0, fmt.Errorf("workload %q network namespace changed", p.ID)
	}
	return p.Network.PID, nil
}

func processStartTime(pid int, root string) (uint64, error) {
	if pid <= 0 || root == "" {
		return 0, fmt.Errorf("process binding requires a positive PID and proc root")
	}
	fs, err := procfs.NewFS(root)
	if err != nil {
		return 0, err
	}
	process, err := fs.Proc(pid)
	if err != nil {
		return 0, err
	}
	stat, err := process.Stat()
	if err != nil {
		return 0, err
	}
	return stat.Starttime, nil
}

func validateProcess(pid int, start uint64, root string) error {
	current, err := processStartTime(pid, root)
	if err != nil {
		return err
	}
	if current != start {
		return fmt.Errorf("process %d start time changed", pid)
	}
	return nil
}

func networkInode(pid int, root string) (uint64, error) {
	info, err := os.Stat(filepath.Join(root, strconv.Itoa(pid), "ns", "net"))
	if err != nil {
		return 0, err
	}
	return info.Sys().(*syscall.Stat_t).Ino, nil
}

// bindHostResources never derives a network target from a process or IP alone.
// Missing resources remain independent of usable cgroup bindings.
func bindHostResources(p *Pod, root string) {
	if p.Process != nil {
		location := p.Process
		p.Process = nil
		if start, err := processStartTime(location.PID, root); err == nil && start != 0 {
			p.Process = &HostProcess{PID: location.PID, startTime: start, procRoot: root}
		}
	}
	if p.Network != nil {
		location := p.Network
		p.Network = bindHostNetwork(location, root)
		if p.Network == nil && location.IPAddress != "" {
			p.Network = &HostNetwork{IPAddress: location.IPAddress}
		}
	}
}

func bindHostNetwork(location *HostNetwork, root string) *HostNetwork {
	start, err := processStartTime(location.PID, root)
	if err != nil || start == 0 {
		return nil
	}
	inode, err := networkInode(location.PID, root)
	if err != nil || inode == 0 || (location.NamespaceInum != 0 && inode != location.NamespaceInum) {
		return nil
	}
	var cookie uint64
	// The existing cookie probe uses /proc; a test or relocated proc tree must
	// never cause it to inspect an unrelated process in the local namespace.
	if root == "/proc" {
		cookie, _ = netutil.NetNamespaceCookieByPID(location.PID)
	}
	if err := validateProcess(location.PID, start, root); err != nil {
		return nil
	}
	currentInode, err := networkInode(location.PID, root)
	if err != nil || currentInode != inode {
		return nil
	}
	return &HostNetwork{
		PID: location.PID, NamespaceInum: inode, NamespaceCookie: cookie,
		IPAddress: location.IPAddress, startTime: start, procRoot: root,
	}
}

// sameResources ignores labels and addresses that do not grant collection
// capabilities. It compares published metadata without kernel I/O under the lock.
func (p *Pod) sameResources(other *Pod) bool {
	if !maps.Equal(p.Cgroups.directories, other.Cgroups.directories) || !maps.Equal(p.Cgroups.CSS, other.Cgroups.CSS) ||
		!maps.Equal(p.Cgroups.kernelGroups, other.Cgroups.kernelGroups) {
		return false
	}
	if (p.Process == nil) != (other.Process == nil) {
		return false
	}
	if p.Process != nil && *p.Process != *other.Process {
		return false
	}
	var network, nextNetwork HostNetwork
	if p.Network != nil {
		network = *p.Network
	}
	if other.Network != nil {
		nextNetwork = *other.Network
	}
	network.IPAddress, nextNetwork.IPAddress = "", ""
	return network == nextNetwork
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
	view.Cgroups.kernelGroups = maps.Clone(p.Cgroups.kernelGroups)
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

// containerFromWorkload builds the existing external shape from common metadata.
// Binding readiness controls CSS; missing process/netns targets stay unavailable.
func containerFromWorkload(discovered, bound *Pod) *Container {
	if bound.Container == nil || (IsKataRuntime(bound.Container.RuntimeType) && bound.Container.SandboxID == "") {
		return nil
	}
	// Legacy document and metric builders require a string namespace label.
	if _, ok := bound.Labels[LabelHostNamespace].(string); !ok {
		return nil
	}
	container := &Container{
		ID: bound.ID, Name: bound.Name, StartedAt: bound.StartedAt,
		SyncedAt: time.Now(), Labels: bound.Labels, CgroupCss: bound.Cgroups.CSS,
	}
	if info := bound.Container; info != nil {
		container.Hostname, container.Type, container.Qos = info.Hostname, info.Type, info.Qos
		container.RuntimeType, container.SandboxID = info.RuntimeType, info.SandboxID
	}
	var cpuPresent bool
	container.CgroupPath, cpuPresent = discovered.Cgroups.Controllers["cpu"]
	if !cpuPresent {
		container.CgroupPath = discovered.Cgroups.Unified
	}
	memory, memoryPresent := discovered.Cgroups.Controllers["memory"]
	if !memoryPresent {
		memory = discovered.Cgroups.Unified
	}
	if bound.Container.CgroupPath != "" {
		container.CgroupPath = bound.Container.CgroupPath
		memory = bound.Container.CgroupPath
	}
	if bound.Container.Guest {
		memory = bound.Container.MemoryCgroupPath
	}
	// Only Guest scanning has an existing explicit unavailable-memory contract.
	// All other empty paths must not reach legacy collectors as the host root.
	for i, membership := range []string{container.CgroupPath, memory} {
		if i == 1 && membership == "" && bound.Container.Guest {
			continue
		}
		if membership == "/" || !filepath.IsAbs(membership) || filepath.Clean(membership) != membership {
			return nil
		}
	}
	container.memoryCgroupPath = &memory
	if discovered.Process != nil {
		container.InitPid = discovered.Process.PID
	}
	container.applyResources(bound)
	return container
}

// applyResources keeps the legacy view on the same verified targets as Pod.
func (c *Container) applyResources(bound *Pod) {
	c.CgroupCss = bound.Cgroups.CSS
	if c.CgroupCss == nil {
		c.CgroupCss = map[string]uint64{}
	}
	c.NetNamespaceInum, c.NetNamespaceCookie = 0, 0
	if bound.Network != nil {
		c.IPAddress = bound.Network.IPAddress
		// The legacy shape has only one PID and cannot represent a separate network target.
		if c.HostKataSandboxID() == "" && bound.Network.PID > 0 && bound.Network.PID == c.InitPid {
			c.NetNamespaceInum, c.NetNamespaceCookie = bound.Network.NamespaceInum, bound.Network.NamespaceCookie
		}
	}
	if c.HostKataSandboxID() != "" {
		c.CgroupCss = map[string]uint64{}
		if css := bound.Cgroups.CSS["cpu"]; css != 0 {
			c.CgroupCss["cpu"] = css
		}
	}
}

// GuestPeerByID returns the peer and workload instance from the same record.
// Callers can reject a connection if the subscribed instance is no longer current.
func GuestPeerByID(id string) (PodKey, GuestPeer, bool) {
	containerView.mu.RLock()
	defer containerView.mu.RUnlock()
	if record := containerView.records[id]; record != nil && record.guestPeer.PID > 0 {
		return record.ref.Key, record.guestPeer, true
	}
	return PodKey{}, GuestPeer{}, false
}
