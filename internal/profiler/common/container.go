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

package utils

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"huatuo-bamai/internal/procfs"
)

// IsProcessInContainer reports whether the process with the given pid runs
// inside a container, by checking its mount namespace and cgroup path.
func IsProcessInContainer(pid int) (bool, error) {
	inNS, err := isInDifferentMountNS(pid)
	if err != nil {
		return false, err
	}

	inCG, err := isInContainerByCgroup(pid)
	if err != nil {
		return false, err
	}

	return inNS || inCG, nil
}

func isInDifferentMountNS(pid int) (bool, error) {
	hostNS, err := os.Readlink(procfs.Path("1", "ns/mnt"))
	if err != nil {
		return false, fmt.Errorf("read host mnt ns: %w", err)
	}

	procNS, err := os.Readlink(procfs.Path(strconv.Itoa(pid), "ns/mnt"))
	if err != nil {
		return false, fmt.Errorf("read proc[%d] mnt ns: %w", pid, err)
	}

	return hostNS != procNS, nil
}

func isInContainerByCgroup(pid int) (bool, error) {
	proc, err := procfs.NewProc(pid)
	if err != nil {
		return false, err
	}

	cgroups, err := proc.Cgroups()
	if err != nil {
		return false, err
	}

	for _, cgroup := range cgroups {
		path := cgroup.Path
		if strings.Contains(path, "docker") ||
			strings.Contains(path, "kubepods") ||
			strings.Contains(path, "containerd") ||
			strings.Contains(path, "cri-containerd") ||
			strings.Contains(path, "crio") ||
			strings.Contains(path, "libpod") {
			return true, nil
		}
	}

	return false, nil
}
