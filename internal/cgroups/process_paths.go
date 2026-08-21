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

package cgroups

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"

	"huatuo-bamai/internal/procfs"
)

// ProcessPaths contains the cgroup membership paths reported for a process.
type ProcessPaths struct {
	Unified     string
	Controllers map[string]string
}

// PathsForPID reads the kernel cgroup membership of pid.
func PathsForPID(pid int) (*ProcessPaths, error) {
	if pid <= 0 {
		return nil, fmt.Errorf("invalid pid %d", pid)
	}

	filePath := procfs.Path(strconv.Itoa(pid), "cgroup")
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open pid %d cgroup: %w", pid, err)
	}
	defer file.Close()

	paths, err := parseProcessPaths(file)
	if err != nil {
		return nil, fmt.Errorf("parse pid %d cgroup: %w", pid, err)
	}
	return paths, nil
}

// PathForProcesses returns the path used to read cgroup.procs on this host.
func (p *ProcessPaths) PathForProcesses() (string, error) {
	if p == nil {
		return "", fmt.Errorf("nil process paths")
	}
	if p.Unified != "" {
		return p.Unified, nil
	}
	for _, controller := range []string{"cpu", "cpuacct", "pids"} {
		if value := p.Controllers[controller]; value != "" {
			return value, nil
		}
	}
	return "", fmt.Errorf("process cgroup path not found")
}

func parseProcessPaths(reader io.Reader) (*ProcessPaths, error) {
	result := &ProcessPaths{}
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()
		_, entry, found := strings.Cut(line, ":")
		if !found {
			return nil, fmt.Errorf("invalid cgroup entry %q", line)
		}
		controllers, membershipPath, found := strings.Cut(entry, ":")
		if !found || membershipPath == "" {
			return nil, fmt.Errorf("invalid cgroup entry %q", line)
		}

		cgroupPath := path.Clean("/" + strings.TrimPrefix(membershipPath, "/"))
		if controllers == "" {
			result.Unified = cgroupPath
			continue
		}
		if result.Controllers == nil {
			result.Controllers = make(map[string]string)
		}
		for _, controller := range strings.Split(controllers, ",") {
			if controller != "" {
				result.Controllers[controller] = cgroupPath
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan cgroup: %w", err)
	}
	if result.Unified == "" && len(result.Controllers) == 0 {
		return nil, fmt.Errorf("empty cgroup membership")
	}
	return result, nil
}
