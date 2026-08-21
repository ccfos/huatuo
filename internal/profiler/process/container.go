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
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"huatuo-bamai/internal/cgroups"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/procfs"
)

// ExecutableFilter identifies processes by executable name and path.
type ExecutableFilter struct {
	// ExecutableName matches the executable basename exactly.
	ExecutableName string
	// ExecutablePrefix matches the beginning of the executable basename.
	ExecutablePrefix string
	// ExecutablePath matches either the executable or an exact command-line argument.
	ExecutablePath string
}

type procInfo struct {
	pid  int
	ppid int
}

// ContainerRootPIDs returns one root PID per matching process tree in containerID.
func ContainerRootPIDs(containerID string, filter ExecutableFilter) ([]int, error) {
	if err := filter.validate(); err != nil {
		return nil, err
	}

	cgroupPath, err := pod.ContainerCgroupPathByID(containerID)
	if err != nil {
		return nil, err
	}

	pidMap, err := findProcessesInCgroups(cgroupPath, filter)
	if err != nil {
		return nil, fmt.Errorf("scan container %q processes: %w", containerID, err)
	}

	rootPIDs := make([]int, 0, len(pidMap))
	for pid := range pidMap {
		rootPIDs = append(rootPIDs, pid)
	}
	slices.Sort(rootPIDs)

	return rootPIDs, nil
}

func (f ExecutableFilter) validate() error {
	hasName := f.ExecutableName != ""
	hasPrefix := f.ExecutablePrefix != ""
	if hasName == hasPrefix {
		return errors.New("set exactly one executable name or prefix")
	}

	return nil
}

func (f ExecutableFilter) matchesName(name string) bool {
	if f.ExecutableName != "" {
		return name == f.ExecutableName
	}

	return strings.HasPrefix(name, f.ExecutablePrefix)
}

// findProcessesInCgroups keeps one root per matching process tree so callers
// start only one profiler for that tree.
func findProcessesInCgroups(cgroupSuffix string, filter ExecutableFilter) (map[int][]int, error) {
	cgroup, err := cgroups.NewManager()
	if err != nil {
		return nil, err
	}

	pids, err := cgroup.Procs(cgroupSuffix)
	if err != nil {
		return nil, err
	}
	procFS, err := procfs.NewDefaultFS()
	if err != nil {
		return nil, fmt.Errorf("open procfs: %w", err)
	}

	return findProcesses(pids, procFS, filter)
}

func findProcesses(pids []int32, procFS procfs.FS, filter ExecutableFilter) (map[int][]int, error) {
	targetProcs := make([]procInfo, 0, len(pids))
	for _, rawPID := range pids {
		pid := int(rawPID)

		proc, err := procFS.Proc(pid)
		if processNoLongerExists(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("open proc for PID %d: %w", pid, err)
		}

		resolvedExecutable, err := proc.Executable()
		if processNoLongerExists(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read executable for PID %d: %w", pid, err)
		}
		if resolvedExecutable == "" {
			continue
		}
		// The kernel's unlinked-file marker does not change executable identity.
		resolvedExecutable = strings.TrimSuffix(resolvedExecutable, " (deleted)")

		if !filter.matchesName(filepath.Base(resolvedExecutable)) {
			continue
		}

		pathMatches, err := executablePathMatches(proc, resolvedExecutable, filter.ExecutablePath)
		if processNoLongerExists(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read cmdline for PID %d: %w", pid, err)
		}
		if !pathMatches {
			continue
		}

		stat, err := proc.Stat()
		if processNoLongerExists(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read stat for PID %d: %w", pid, err)
		}

		targetProcs = append(targetProcs, procInfo{pid: pid, ppid: stat.PPID})
	}

	return groupProcessesByRoot(targetProcs)
}

func groupProcessesByRoot(processes []procInfo) (map[int][]int, error) {
	parents := make(map[int]int, len(processes))
	for _, proc := range processes {
		parents[proc.pid] = proc.ppid
	}

	result := make(map[int][]int)
	for _, proc := range processes {
		root := proc.pid
		parent := proc.ppid
		seen := map[int]struct{}{proc.pid: {}}

		for {
			next, ok := parents[parent]
			if !ok {
				break
			}
			if _, ok := seen[parent]; ok {
				return nil, fmt.Errorf("parent cycle at PID %d", parent)
			}

			seen[parent] = struct{}{}
			root = parent
			parent = next
		}

		result[root] = append(result[root], proc.pid)
	}

	return result, nil
}

// executablePathMatches handles interpreters launched indirectly, where
// /proc/<pid>/exe differs from the requested executable path.
func executablePathMatches(proc procfs.Proc, resolvedExecutable, executablePath string) (bool, error) {
	if executablePath == "" || resolvedExecutable == executablePath {
		return true, nil
	}

	cmdline, err := proc.CmdLine()
	if err != nil {
		return false, err
	}

	for _, arg := range cmdline {
		if arg == executablePath {
			return true, nil
		}
	}

	return false, nil
}

func processNoLongerExists(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH)
}
