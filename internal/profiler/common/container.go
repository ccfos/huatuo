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
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"huatuo-bamai/internal/cgroups"
	"huatuo-bamai/internal/command/container"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/procfs"

	"github.com/shirou/gopsutil/process"
)

func ContainerPathOnHost(pid int, containerPath string) string {
	if containerPath == "" {
		return ""
	}
	return fmt.Sprintf("/proc/%d/root%s", pid, containerPath)
}

func ProcRootPath(pid int) string {
	return filepath.Join("/proc", strconv.Itoa(pid), "root")
}

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

func GetPidsFromContainer(bamaiSvr, execPath, langKeyWord, containerID string) ([]int, error) {
	c, err := container.GetContainerByID(bamaiSvr, containerID)
	if err != nil {
		return nil, err
	}

	ccSuffix := c.CgroupPath

	pidMap, err := findProcessesInCgroups(ccSuffix, langKeyWord, execPath)
	if err != nil {
		return nil, err
	}
	ppids := make([]int, 0, len(pidMap))
	for k := range pidMap {
		ppids = append(ppids, k)
	}
	return ppids, nil
}

func findProcessesInCgroups(cCgroupsSuffix, langKeyword, execPath string) (map[int][]int, error) {
	cgroup, err := cgroups.NewManager()
	if err != nil {
		return nil, err
	}

	pids, err := cgroup.Procs(cCgroupsSuffix)
	if err != nil {
		return nil, err
	}

	type procInfo struct {
		pid  int
		ppid int
	}

	validPids := make(map[int]bool)
	targetProcs := make([]procInfo, 0)

	for _, rawPid := range pids {
		pid := int(rawPid)

		exeLink := fmt.Sprintf("/proc/%d/exe", pid)
		resolvedExe, err := os.Readlink(exeLink)
		if err != nil {
			continue
		}

		base := filepath.Base(resolvedExe)
		if !strings.HasPrefix(base, langKeyword) {
			continue
		}

		if execPath != "" {
			log.P().Infof("exec-path is provided, matching the process exec-path...")

			if resolvedExe != execPath {
				p, err := process.NewProcess(int32(pid))
				if err != nil {
					log.P().Errorf("Failed to create process for PID %d: %v", pid, err)
					continue
				}

				cmdlineSlice, err := p.CmdlineSlice()
				if err != nil {
					log.P().Errorf("Failed to get cmdline for PID %d: %v", pid, err)
					continue
				}

				foundExecPathInCmdline := false
				for _, arg := range cmdlineSlice {
					if strings.Contains(arg, execPath) {
						foundExecPathInCmdline = true
						break
					}
				}

				if !foundExecPathInCmdline {
					continue
				}
			}
		}

		statusPath := fmt.Sprintf("/proc/%d/status", pid)
		statusData, err := os.ReadFile(statusPath)
		if err != nil {
			continue
		}

		var ppid int
		scanner := bufio.NewScanner(bytes.NewReader(statusData))
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "PPid:") {
				_, err := fmt.Sscanf(line, "PPid:\t%d", &ppid)
				if err != nil {
					return nil, fmt.Errorf("failed to parse PPid: %w", err)
				}
				break
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("error reading status data: %w", err)
		}

		targetProcs = append(targetProcs, procInfo{pid: pid, ppid: ppid})
		validPids[pid] = true
	}

	result := make(map[int][]int)
	for _, proc := range targetProcs {
		if validPids[proc.ppid] {
			result[proc.ppid] = append(result[proc.ppid], proc.pid)
		} else {
			result[proc.pid] = append(result[proc.pid], proc.pid)
		}
	}

	for parent, children := range result {
		log.P().Infof("Parent PID: %d -> Children: %v", parent, children)
	}

	return result, nil
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
