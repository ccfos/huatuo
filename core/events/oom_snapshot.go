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

package events

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"huatuo-bamai/internal/cgroups"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/procfs"
)

var oomHostMemInfoKeys = map[string]bool{
	"MemTotal":       true,
	"MemFree":        true,
	"MemAvailable":   true,
	"Buffers":        true,
	"Cached":         true,
	"SwapCached":     true,
	"Active(anon)":   true,
	"Inactive(anon)": true,
	"Active(file)":   true,
	"Inactive(file)": true,
	"Unevictable":    true,
	"Slab":           true,
	"SReclaimable":   true,
	"SUnreclaim":     true,
	"SwapTotal":      true,
	"SwapFree":       true,
	"CommitLimit":    true,
	"Committed_AS":   true,
}

type OOMMemorySnapshot struct {
	HostMemInfo map[string]uint64 `json:"host_meminfo,omitempty"`
}

type OOMCgroupMemorySnapshot struct {
	Path    string            `json:"path,omitempty"`
	Current uint64            `json:"current,omitempty"`
	Max     uint64            `json:"max,omitempty"`
	Stat    map[string]uint64 `json:"stat,omitempty"`
	Events  map[string]uint64 `json:"events,omitempty"`
}

func hostMemorySnapshot() (*OOMMemorySnapshot, error) {
	hostMemInfo, err := readOOMHostMemInfo(procfs.Path("meminfo"), oomHostMemInfoKeys)
	if err != nil {
		return nil, fmt.Errorf("host meminfo: %w", err)
	}

	return &OOMMemorySnapshot{HostMemInfo: hostMemInfo}, nil
}

func cgroupMemorySnapshot(cgroup cgroups.Cgroup, container *pod.Container) (*OOMCgroupMemorySnapshot, error) {
	snapshot := &OOMCgroupMemorySnapshot{
		Path: container.CgroupPath,
	}

	usage, err := cgroup.MemoryUsage(container.CgroupPath)
	if err != nil {
		return nil, fmt.Errorf("memory usage: %w", err)
	}
	if usage != nil {
		snapshot.Current = usage.Usage
		snapshot.Max = usage.MaxLimited
	}

	stat, err := cgroup.MemoryStatRaw(container.CgroupPath)
	if err != nil {
		return nil, fmt.Errorf("memory.stat: %w", err)
	}
	snapshot.Stat = stat

	events, err := cgroup.MemoryEventRaw(container.CgroupPath)
	if err != nil {
		return nil, fmt.Errorf("memory.events: %w", err)
	}
	snapshot.Events = events

	return snapshot, nil
}

func readOOMHostMemInfo(path string, required map[string]bool) (map[string]uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	result := make(map[string]uint64, len(required))
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		key, value, ok := strings.Cut(line, ":")
		if !ok || !required[key] {
			continue
		}

		parsed, ok := parseKiBField(value)
		if !ok {
			continue
		}
		result[key] = parsed
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func parseKiBField(value string) (uint64, bool) {
	parsed, ok := parseUintField(value)
	if !ok {
		return 0, false
	}
	return parsed * 1024, true
}

func parseUintField(value string) (uint64, bool) {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return 0, false
	}

	parsed, err := strconv.ParseUint(fields[0], 10, 64)
	return parsed, err == nil
}
