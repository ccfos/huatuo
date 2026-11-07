// Copyright 2025, 2026 The HuaTuo Authors
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

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/procfs"
)

// containerMemoryBinding resolves kernel membership rather than reconstructing
// the memory hierarchy from the kubelet's cgroup driver or container name.
func containerMemoryBinding(container *Container) (*containerRecord, error) {
	// Reference validation can pass a published view; never mutate its resources.
	view := *container
	container = &view
	process, err := procfs.NewProc(container.InitPid)
	if err != nil {
		return nil, err
	}
	before, err := process.Stat()
	if err != nil {
		return nil, err
	}
	paths, err := cgroups.PathsForPID(container.InitPid)
	if err != nil {
		return nil, err
	}
	mounts, err := cgroupMounts()
	if err != nil {
		return nil, err
	}
	locations := CgroupBinding{Unified: paths.Unified, Controllers: paths.Controllers}
	workload := podFromContainer(container)
	if container.HostKataSandboxID() == "" {
		workload.Network = &HostNetwork{PID: container.InitPid, IPAddress: container.IPAddress}
	} else {
		workload.Network = &HostNetwork{IPAddress: container.IPAddress}
	}
	record := &containerRecord{
		container: container, pod: workload, cgroupLocations: locations,
		ref:       ContainerRef{Key: ContainerKey{ID: container.ID}, InitPID: container.InitPid},
		startTime: before.Starttime,
	}
	workload.Cgroups = bindCgroups(record.cgroupLocations, mounts)
	bindHostResources(workload, procfs.DefaultPath())
	path, memoryErr := paths.PathForMemory()
	var info os.FileInfo
	if memoryErr == nil {
		controller := "memory"
		if paths.Controllers[controller] == "" {
			controller = ""
		}
		_, info, memoryErr = resolveCgroupDirectory(mounts, controller, path)
	}
	after, err := process.Stat()
	if err != nil {
		return nil, err
	}
	if before.Starttime != after.Starttime {
		return nil, fmt.Errorf("container %q init process was replaced", container.ID)
	}
	container.applyResources(workload)

	if memoryErr != nil {
		return record, memoryErr
	}
	// A hypervisor's memory cgroup describes the shared VM, not each Guest
	// application. Guest discovery supplies application memory bindings.
	if container.HostKataSandboxID() != "" {
		path = ""
	}
	record.ref.MemoryCgroupPath = path
	record.directory = info
	return record, nil
}

// ValidateContainerResources verifies the published generation and the live process
// and cgroup binding. Runtime-specific checks belong to the source; this function only reads local kernel state.
func ValidateContainerResources(ref ContainerRef) error {
	containerView.mu.RLock()
	previous, err := containerView.records[ref.Key.ID], containerView.err
	containerView.mu.RUnlock()
	if err != nil {
		return err
	}
	if previous == nil || previous.ref != ref {
		return fmt.Errorf("container %q instance is no longer current", ref.Key.ID)
	}
	if previous.pod.Container != nil && previous.pod.Container.Guest {
		if err := validateGuestContainerRef(previous); err != nil {
			return err
		}
	} else {
		current, err := containerMemoryBinding(previous.container)
		if err != nil {
			return err
		}
		if !previous.sameInstance(current) {
			return fmt.Errorf("container %q kernel binding changed", ref.Key.ID)
		}
	}
	// A lifecycle commit may have raced with the kernel reads.
	actual, err := ContainerRefByID(ref.Key.ID)
	if err != nil {
		return err
	}
	if actual != ref {
		return fmt.Errorf("container %q generation changed", ref.Key.ID)
	}
	return nil
}
