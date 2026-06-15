// Copyright 2025 The HuaTuo Authors
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
)

const (
	defaultSystemdSuffix  = ".slice"
	defaultNodeCgroupName = "kubepods"
)

// {"kubepods", "burstable", "pod1234-abcd-5678-efgh"}
type cgroupPath []string

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
func (paths cgroupPath) ToSystemd() string {
	newparts := []string{}
	for _, part := range paths {
		part = escapeSystemd(part)
		newparts = append(newparts, part)
	}

	return expandSystemdSlice(strings.Join(newparts, "-") + defaultSystemdSuffix)
}

func (paths cgroupPath) ToCgroupfs() string {
	return "/" + path.Join(paths...)
}

func containerCgroupPath(containerID string, pod *corev1.Pod) cgroupPath {
	paths := []string{defaultNodeCgroupName}

	if pod.Status.QOSClass != corev1.PodQOSGuaranteed {
		paths = append(paths, strings.ToLower(string(pod.Status.QOSClass)))
	}

	paths = append(paths, fmt.Sprintf("pod%s", pod.UID))

	if kubeletPodCgroupDriver != "systemd" {
		paths = append(paths, containerID)
	}

	return paths
}

// https://github.com/kubernetes/kubernetes/blob/master/pkg/kubelet/cm/cgroup_manager_linux.go#L81
func containerCgroupSuffix(containerID string, pod *corev1.Pod) (string, error) {
	name := containerCgroupPath(containerID, pod)

	if kubeletPodCgroupDriver == "systemd" {
		scope, err := containerScopeName(containerID)
		if err != nil {
			return "", fmt.Errorf("container scope name: %w", err)
		}
		return name.ToSystemd() + "/" + scope, nil
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
