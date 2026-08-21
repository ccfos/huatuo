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
	"path"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"huatuo-bamai/internal/cgroups"
)

const (
	defaultSystemdSuffix  = ".slice"
	defaultNodeCgroupName = "kubepods"
)

// slices: {"kubepods", "burstable", "pod1234-abcd-5678-efgh"}
// scope: systemd scope unit appended after the slice hierarchy (systemd driver only).
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
