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

	"github.com/ccfos/huatuo/internal/procfs"
)

// ProcessPaths contains the cgroup membership paths reported for a process.
type ProcessPaths struct {
	Unified     string
	Controllers map[string]string
}

// PathForMemory returns the memory-controller membership, including on hybrid hosts.
func (p *ProcessPaths) PathForMemory() (string, error) {
	if p == nil {
		return "", fmt.Errorf("nil process paths")
	}
	if path := p.Controllers["memory"]; path != "" {
		return path, nil
	}
	if p.Unified != "" {
		return p.Unified, nil
	}
	return "", fmt.Errorf("process memory cgroup path not found")
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
	return p.pathForProcesses(CgroupMode())
}

// pathForProcesses resolves the membership for the manager that mode selects.
// NewManager() returns the v1 manager on legacy and hybrid hosts and the v2
// manager on unified hosts, and each manager reads the returned path from its
// own hierarchy: /sys/fs/cgroup/<controller>/<path>/cgroup.procs against
// /sys/fs/cgroup/<path>/cgroup.procs.
//
// A hybrid host reports both memberships in /proc/<pid>/cgroup, and only the
// controller entry is meaningful there, because hybrid hosts keep the resource
// controllers on the v1 hierarchies and the unified hierarchy carries none.
// The other membership is kept as a fallback so that a host whose layout does
// not match the mode it is classified as still resolves something.
func (p *ProcessPaths) pathForProcesses(mode Mode) (string, error) {
	if p == nil {
		return "", fmt.Errorf("nil process paths")
	}

	if mode == Unified {
		if p.Unified != "" {
			return p.Unified, nil
		}
	}
	if path := p.controllerMembership(); path != "" {
		return path, nil
	}
	if p.Unified != "" {
		return p.Unified, nil
	}
	return "", fmt.Errorf("process cgroup path not found")
}

func (p *ProcessPaths) controllerMembership() string {
	for _, controller := range []string{"cpu", "cpuacct", "pids"} {
		if value := p.Controllers[controller]; value != "" {
			return value
		}
	}
	return ""
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
