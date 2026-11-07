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

package container

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	extcgroups "github.com/containerd/cgroups/v3"
	corev1 "k8s.io/api/core/v1"

	"github.com/ccfos/huatuo/internal/cgroups"
	podmodel "github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/procfs"
)

const (
	defaultSystemdSuffix  = ".slice"
	defaultNodeCgroupName = "kubepods"
)

type cgroupPath struct {
	slices []string
	scope  string
}

func escapeSystemd(part string) string {
	return strings.ReplaceAll(part, "-", "_")
}

// systemd represents slice hierarchy using `-`, so we need to follow suit when
// generating the path of slice.
// Essentially, test-a-b.slice becomes /test.slice/test-a.slice/test-a-b.slice.
func expandSystemdSlice(slice string) string {
	var path, prefix string

	sliceName := strings.TrimSuffix(slice, defaultSystemdSuffix)
	for _, component := range strings.Split(sliceName, "-") {
		// Append the component to the path and to the prefix.
		path += "/" + prefix + component + defaultSystemdSuffix
		prefix += component + "-"
	}

	return path
}

// {"kubepods", "burstable", "pod1234-abcd-5678-efgh"} becomes
// "/kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod1234_abcd_5678_efgh.slice"
// with the scope unit appended when present.
func (p cgroupPath) ToSystemd() string {
	newparts := []string{}
	for _, part := range p.slices {
		part = escapeSystemd(part)
		newparts = append(newparts, part)
	}

	slicePath := expandSystemdSlice(strings.Join(newparts, "-") + defaultSystemdSuffix)

	if p.scope != "" {
		return slicePath + "/" + p.scope
	}

	return slicePath
}

func (p cgroupPath) ToCgroupfs() string {
	return "/" + path.Join(p.slices...)
}

func containerCgroupPathsByID(containerID string) (*cgroups.ProcessPaths, error) {
	pid, err := containerInitPIDByID(containerID)
	if err != nil {
		return nil, err
	}

	paths, err := cgroups.PathsForPID(pid)
	if err != nil {
		return nil, fmt.Errorf("resolve container %q cgroup paths from pid %d: %w", containerID, pid, err)
	}
	return paths, nil
}

func containerCgroupSuffix(containerID string, pod *corev1.Pod) (string, error) {
	name, err := containerCgroupPath(containerID, pod)
	if err != nil {
		return "", err
	}

	if kubeletPodCgroupDriver == "systemd" {
		return name.ToSystemd(), nil
	}

	return name.ToCgroupfs(), nil
}

func containerScopeName(containerID string) (string, error) {
	switch currContainerProvider {
	case containerProviderDocker:
		return "docker-" + containerID + ".scope", nil
	case containerProviderContainerd:
		return "cri-containerd-" + containerID + ".scope", nil
	default:
		return "", fmt.Errorf("container provider not initialized")
	}
}

// ContainerCgroupPathByID returns the path used to read the container's processes.
func ContainerCgroupPathByID(containerID string) (string, error) {
	paths, err := containerCgroupPathsByID(containerID)
	if err != nil {
		return "", err
	}
	cgroupPath, err := paths.PathForProcesses()
	if err != nil {
		return "", fmt.Errorf("resolve container %q process cgroup path: %w", containerID, err)
	}
	return cgroupPath, nil
}

// ContainerMemoryCgroupPathByID resolves the memory hierarchy without a kubelet refresh.
func ContainerMemoryCgroupPathByID(containerID string) (string, error) {
	paths, err := containerCgroupPathsByID(containerID)
	if err != nil {
		return "", err
	}
	path := paths.Controllers["memory"]
	if cgroups.CgroupMode() == cgroups.Unified {
		path = paths.Unified
	}
	if path == "" {
		return "", fmt.Errorf("container %q has no memory cgroup path", containerID)
	}
	return path, nil
}

// https://github.com/kubernetes/kubernetes/blob/master/pkg/kubelet/cm/cgroup_manager_linux.go#L81
func containerCgroupPath(containerID string, pod *corev1.Pod) (cgroupPath, error) {
	paths := []string{defaultNodeCgroupName}

	if pod.Status.QOSClass != corev1.PodQOSGuaranteed {
		paths = append(paths, strings.ToLower(string(pod.Status.QOSClass)))
	}

	paths = append(paths, fmt.Sprintf("pod%s", pod.UID))

	if kubeletPodCgroupDriver == "systemd" {
		scope, err := containerScopeName(containerID)
		if err != nil {
			return cgroupPath{}, fmt.Errorf("container scope name: %w", err)
		}
		return cgroupPath{slices: paths, scope: scope}, nil
	}

	paths = append(paths, containerID)
	return cgroupPath{slices: paths}, nil
}

// populateKataContainer locates the Host VM without granting collection targets.
// CSS and process readiness are verified by the common resource binding layer.
func populateKataContainer(container *podmodel.Container, info *RuntimeInfo) error {
	if info.PID <= 0 {
		return fmt.Errorf("invalid Kata hypervisor pid %d for container %s", info.PID, container.ID)
	}
	if info.SandboxID == "" {
		return fmt.Errorf("Kata container %s has no sandbox ID", container.ID)
	}
	cgroupPath, err := kataCgroupPath(procfs.Path(fmt.Sprint(info.PID), "cgroup"), cgroups.CgroupMode())
	if err != nil {
		return fmt.Errorf("resolve Kata hypervisor cgroup: %w", err)
	}
	container.InitPid = info.PID
	container.SandboxID = info.SandboxID
	container.CgroupPath = cgroupPath
	// The VM's net namespace is not the Guest container's net namespace, so
	// NetNamespaceInum stays zero for Host Kata records.
	return nil
}

func kataCgroupPath(procCgroupFile string, mode cgroups.Mode) (string, error) {
	legacy, unified, err := extcgroups.ParseCgroupFileUnified(procCgroupFile)
	if err != nil {
		return "", err
	}
	cgroupPath := unified
	root := cgroups.RootfsDefaultPath()
	files := []string{"cpu.stat", "cpu.max"}
	switch mode {
	case cgroups.Unified:
	case cgroups.Legacy, cgroups.Hybrid:
		cgroupPath = legacy["cpu"]
		// podmodel.Container currently has one cgroup path shared by CPU and memory.
		// Do not silently apply one controller's path to a different hierarchy.
		for _, controller := range []string{"cpuacct", "memory"} {
			if legacy[controller] != cgroupPath {
				return "", fmt.Errorf("Kata %s cgroup path differs from CPU cgroup path", controller)
			}
		}
		root = cgroups.RootFsFilePath("cpu")
		files = []string{"cpuacct.stat", "cpuacct.usage", "cpu.cfs_quota_us", "cpu.cfs_period_us"}
	default:
		return "", fmt.Errorf("unsupported Kata cgroup mode %d", mode)
	}
	if !filepath.IsAbs(cgroupPath) || cgroupPath == "/" || filepath.Clean(cgroupPath) != cgroupPath {
		return "", fmt.Errorf("invalid Kata cgroup path %q", cgroupPath)
	}
	for _, name := range files {
		if _, err := os.ReadFile(filepath.Join(root, cgroupPath, name)); err != nil {
			return "", err
		}
	}
	return cgroupPath, nil
}

func cgroupMembershipForSubsystem(paths *cgroups.ProcessPaths, mode cgroups.Mode, subsysName string) (string, error) {
	if paths == nil {
		return "", errors.New("missing process cgroup membership")
	}
	switch mode {
	case cgroups.Legacy, cgroups.Hybrid:
		membershipPath := paths.Controllers[subsysName]
		if membershipPath == "" {
			return "", fmt.Errorf("no %s cgroup v1 membership", subsysName)
		}
		// A v1 hierarchy only reports its own controllers. Reading CPU's
		// notification file cannot populate a missing memory CSS entry.
		return membershipPath, nil
	case cgroups.Unified:
		if paths.Unified == "" {
			return "", errors.New("no cgroup v2 membership")
		}
		return paths.Unified, nil
	default:
		return "", fmt.Errorf("unsupported cgroup mode %d", mode)
	}
}
