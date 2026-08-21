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

package process

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"huatuo-bamai/internal/procfs"
)

// IsInContainer reports whether pid has a recognized container cgroup path.
// Container membership is a userspace convention, so the result is heuristic.
func IsInContainer(pid int) (bool, error) {
	return isInContainerCgroup(pid)
}

// HasDifferentMountNamespace reports whether pid and host PID 1 use different
// mount namespaces.
func HasDifferentMountNamespace(pid int) (bool, error) {
	hostNamespace, err := os.Readlink(procfs.Path("1", "ns/mnt"))
	if err != nil {
		return false, fmt.Errorf("read PID 1 mount namespace: %w", err)
	}

	processNamespace, err := os.Readlink(procfs.Path(strconv.Itoa(pid), "ns/mnt"))
	if err != nil {
		return false, fmt.Errorf("read PID %d mount namespace: %w", pid, err)
	}

	return hostNamespace != processNamespace, nil
}

func isInContainerCgroup(pid int) (bool, error) {
	proc, err := procfs.NewProc(pid)
	if err != nil {
		return false, fmt.Errorf("open proc for PID %d: %w", pid, err)
	}

	cgroups, err := proc.Cgroups()
	if err != nil {
		return false, fmt.Errorf("read cgroup for PID %d: %w", pid, err)
	}

	for _, cgroup := range cgroups {
		if isContainerCgroupPath(cgroup.Path) {
			return true, nil
		}
	}

	return false, nil
}

func isContainerCgroupPath(path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, part := range parts {
		isKubernetesCgroup := part == "kubepods" ||
			part == "kubepods.slice" ||
			(strings.HasPrefix(part, "kubepods-") && strings.HasSuffix(part, ".slice"))
		if isKubernetesCgroup || isContainerScope(part) {
			return true
		}

		isRuntimeDirectory := part == "docker" || part == "crio" || part == "libpod"
		if isRuntimeDirectory && i+1 < len(parts) && isContainerID(parts[i+1]) {
			return true
		}
	}

	return false
}

func isContainerScope(name string) bool {
	if !strings.HasSuffix(name, ".scope") {
		return false
	}

	for _, prefix := range []string{"docker-", "cri-containerd-", "crio-", "libpod-"} {
		if strings.HasPrefix(name, prefix) {
			id := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".scope")
			return isContainerID(id)
		}
	}

	return false
}

func isContainerID(value string) bool {
	if len(value) < 12 {
		return false
	}

	for _, char := range []byte(value) {
		isDigit := char >= '0' && char <= '9'
		isLowerHex := char >= 'a' && char <= 'f'
		isUpperHex := char >= 'A' && char <= 'F'
		if !isDigit && !isLowerHex && !isUpperHex {
			return false
		}
	}

	return true
}
