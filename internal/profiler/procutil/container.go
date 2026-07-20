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

package procutil

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"huatuo-bamai/internal/cgroups"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/procfs"

	"github.com/shirou/gopsutil/process"
)

// ContainerPathOnHost returns the host-visible path for a container-scoped path.
func ContainerPathOnHost(pid int, containerPath string) string {
	if containerPath == "" {
		return ""
	}
	return fmt.Sprintf("/proc/%d/root%s", pid, containerPath)
}

// ProcRootPath returns the /proc/<pid>/root prefix.
func ProcRootPath(pid int) string {
	return filepath.Join("/proc", strconv.Itoa(pid), "root")
}

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

// GetPidsFromContainer returns the root PIDs of processes in containerID
// that match langKeyword (e.g. "python", "java") and optionally execPath.
func GetPidsFromContainer(execPath, langKeyword, containerID string) ([]int, error) {
	cgroupPath, err := pod.ContainerCgroupPathByID(containerID)
	if err != nil {
		return nil, err
	}

	pidMap, err := findProcessesInCgroups(cgroupPath, langKeyword, execPath)
	if err != nil {
		return nil, err
	}

	ppids := make([]int, 0, len(pidMap))
	for k := range pidMap {
		ppids = append(ppids, k)
	}

	return ppids, nil
}

// findProcessesInCgroups groups processes in the cgroup matching langKeyword
// (and optionally execPath) by their root PID inside the cgroup. Each process
// is placed under its highest matching ancestor. Returns an empty result when
// both langKeyword and execPath fail to match any process.
func findProcessesInCgroups(cgroupSuffix, langKeyword, execPath string) (map[int][]int, error) {
	cgroup, err := cgroups.NewManager()
	if err != nil {
		return nil, err
	}

	pids, err := cgroup.Procs(cgroupSuffix)
	if err != nil {
		return nil, err
	}

	parentByPID := make(map[int]int)

	for _, rawPid := range pids {
		pid := int(rawPid)

		resolvedExe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
		if err != nil {
			continue
		}

		if !strings.HasPrefix(filepath.Base(resolvedExe), langKeyword) {
			continue
		}

		if execPath != "" && resolvedExe != execPath {
			if !execPathInCmdline(pid, execPath) {
				continue
			}
		}

		ppid, err := readPPid(pid)
		if err != nil {
			return nil, err
		}

		parentByPID[pid] = ppid
	}

	return groupProcessesByRoot(parentByPID), nil
}

// groupProcessesByRoot groups matching processes under their highest matching
// ancestor. If a malformed parent graph has a cycle, its lowest PID is used as
// a stable group root so the caller still receives one target for that cycle.
func groupProcessesByRoot(parentByPID map[int]int) map[int][]int {
	result := make(map[int][]int)
	for pid := range parentByPID {
		root := processGroupRoot(pid, parentByPID)
		result[root] = append(result[root], pid)
	}

	for _, pids := range result {
		sort.Ints(pids)
	}

	return result
}

func processGroupRoot(pid int, parentByPID map[int]int) int {
	path := make([]int, 0)
	seen := make(map[int]int)
	current := pid

	for {
		if cycleStart, ok := seen[current]; ok {
			root := path[cycleStart]
			for _, cyclePID := range path[cycleStart+1:] {
				if cyclePID < root {
					root = cyclePID
				}
			}
			return root
		}

		seen[current] = len(path)
		path = append(path, current)

		parent, ok := parentByPID[current]
		if !ok {
			return current
		}
		if _, ok := parentByPID[parent]; !ok {
			return current
		}

		current = parent
	}
}

// execPathInCmdline reports whether execPath appears in any cmdline argument
// of pid. Used as a fallback when /proc/<pid>/exe does not equal execPath
// (e.g. interpreters launched indirectly).
func execPathInCmdline(pid int, execPath string) bool {
	p, err := process.NewProcess(int32(pid))
	if err != nil {
		log.Warnf("new process for pid %d: %v", pid, err)
		return false
	}

	cmdline, err := p.CmdlineSlice()
	if err != nil {
		log.Warnf("read cmdline for pid %d: %v", pid, err)
		return false
	}

	for _, arg := range cmdline {
		if strings.Contains(arg, execPath) {
			return true
		}
	}

	return false
}

func readPPid(pid int) (int, error) {
	statusData, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, fmt.Errorf("read status for pid %d: %w", pid, err)
	}

	scanner := bufio.NewScanner(bytes.NewReader(statusData))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "PPid:") {
			continue
		}

		var ppid int
		if _, err := fmt.Sscanf(line, "PPid:\t%d", &ppid); err != nil {
			return 0, fmt.Errorf("parse PPid for pid %d: %w", pid, err)
		}

		return ppid, nil
	}

	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("scan status for pid %d: %w", pid, err)
	}

	return 0, fmt.Errorf("PPid not found for pid %d", pid)
}

// ParentPID returns the current parent process ID from procfs.
func ParentPID(pid int) (int, error) {
	return readPPid(pid)
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
