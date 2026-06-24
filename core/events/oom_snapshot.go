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
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"huatuo-bamai/internal/cgroups/stats"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/procfs"
)

const oomTopProcessLimit = 10

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

type oomCgroupReader interface {
	MemoryUsage(path string) (*stats.MemoryUsage, error)
	MemoryStatRaw(path string) (map[string]uint64, error)
	MemoryEventRaw(path string) (map[string]uint64, error)
}

type OOMMemorySnapshot struct {
	TopProcesses  []*OOMProcessMemory      `json:"top_processes,omitempty"`
	HostMemInfo   map[string]uint64        `json:"host_meminfo,omitempty"`
	TriggerCgroup *OOMCgroupMemorySnapshot `json:"trigger_cgroup,omitempty"`
	VictimCgroup  *OOMCgroupMemorySnapshot `json:"victim_cgroup,omitempty"`
	Errors        []string                 `json:"errors,omitempty"`
}

type OOMProcessMemory struct {
	PID         int32  `json:"pid"`
	ProcessName string `json:"process_name"`
	VmRSS       uint64 `json:"vm_rss"`
	RssAnon     uint64 `json:"rss_anon"`
	RssFile     uint64 `json:"rss_file"`
	RssShmem    uint64 `json:"rss_shmem"`
	VmSwap      uint64 `json:"vm_swap"`
	Total       uint64 `json:"total"`
}

type OOMCgroupMemorySnapshot struct {
	ContainerID       string            `json:"container_id,omitempty"`
	ContainerHostname string            `json:"container_hostname,omitempty"`
	CgroupPath        string            `json:"cgroup_path,omitempty"`
	Current           uint64            `json:"current,omitempty"`
	Max               uint64            `json:"max,omitempty"`
	Stat              map[string]uint64 `json:"stat,omitempty"`
	Events            map[string]uint64 `json:"events,omitempty"`
	Errors            []string          `json:"errors,omitempty"`
}

func captureOOMMemorySnapshot(cgroup oomCgroupReader, trigger, victim *pod.Container) *OOMMemorySnapshot {
	snapshot := &OOMMemorySnapshot{}

	topProcesses, err := topOOMMemoryProcesses(procfs.DefaultPath(), oomTopProcessLimit)
	if err != nil {
		snapshot.Errors = append(snapshot.Errors, fmt.Sprintf("top processes: %v", err))
	} else {
		snapshot.TopProcesses = topProcesses
	}

	hostMemInfo, err := readOOMHostMemInfo(procfs.Path("meminfo"), oomHostMemInfoKeys)
	if err != nil {
		snapshot.Errors = append(snapshot.Errors, fmt.Sprintf("host meminfo: %v", err))
	} else {
		snapshot.HostMemInfo = hostMemInfo
	}

	if cgroup == nil {
		if trigger != nil || victim != nil {
			snapshot.Errors = append(snapshot.Errors, "cgroup reader is not available")
		}
		return snapshot
	}

	snapshot.TriggerCgroup = captureOOMCgroupMemorySnapshot(cgroup, trigger)
	if victim != nil {
		if trigger != nil && trigger.ID == victim.ID {
			snapshot.VictimCgroup = snapshot.TriggerCgroup
		} else {
			snapshot.VictimCgroup = captureOOMCgroupMemorySnapshot(cgroup, victim)
		}
	}

	return snapshot
}

func captureOOMCgroupMemorySnapshot(cgroup oomCgroupReader, container *pod.Container) *OOMCgroupMemorySnapshot {
	if container == nil {
		return nil
	}

	snapshot := &OOMCgroupMemorySnapshot{
		ContainerID:       container.ID,
		ContainerHostname: container.Hostname,
		CgroupPath:        container.CgroupPath,
	}

	usage, err := cgroup.MemoryUsage(container.CgroupPath)
	if err != nil {
		snapshot.Errors = append(snapshot.Errors, fmt.Sprintf("memory usage: %v", err))
	} else if usage != nil {
		snapshot.Current = usage.Usage
		snapshot.Max = usage.MaxLimited
	}

	stat, err := cgroup.MemoryStatRaw(container.CgroupPath)
	if err != nil {
		snapshot.Errors = append(snapshot.Errors, fmt.Sprintf("memory.stat: %v", err))
	} else {
		snapshot.Stat = stat
	}

	events, err := cgroup.MemoryEventRaw(container.CgroupPath)
	if err != nil {
		snapshot.Errors = append(snapshot.Errors, fmt.Sprintf("memory.events: %v", err))
	} else {
		snapshot.Events = events
	}

	return snapshot
}

func topOOMMemoryProcesses(procRoot string, topN int) ([]*OOMProcessMemory, error) {
	if topN <= 0 {
		return nil, nil
	}

	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, err
	}

	processes := make([]*OOMProcessMemory, 0, topN)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		pid64, err := strconv.ParseInt(entry.Name(), 10, 32)
		if err != nil {
			continue
		}

		process, err := readOOMProcessMemory(filepath.Join(procRoot, entry.Name(), "status"), int32(pid64))
		if err != nil {
			continue
		}
		processes = append(processes, process)
	}

	sort.Slice(processes, func(i, j int) bool {
		if processes[i].Total == processes[j].Total {
			return processes[i].PID < processes[j].PID
		}
		return processes[i].Total > processes[j].Total
	})

	if len(processes) <= topN {
		return processes, nil
	}
	return processes[:topN], nil
}

func readOOMProcessMemory(statusPath string, fallbackPID int32) (*OOMProcessMemory, error) {
	file, err := os.Open(statusPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	process := &OOMProcessMemory{PID: fallbackPID}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}

		switch key {
		case "Name":
			process.ProcessName = strings.TrimSpace(value)
		case "Pid":
			if pid, ok := parseUintField(value); ok {
				process.PID = int32(pid)
			}
		case "VmRSS":
			process.VmRSS, _ = parseKiBField(value)
		case "RssAnon":
			process.RssAnon, _ = parseKiBField(value)
		case "RssFile":
			process.RssFile, _ = parseKiBField(value)
		case "RssShmem":
			process.RssShmem, _ = parseKiBField(value)
		case "VmSwap":
			process.VmSwap, _ = parseKiBField(value)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	process.Total = process.RssAnon + process.RssFile + process.RssShmem + process.VmSwap
	if process.Total == 0 {
		process.Total = process.VmRSS + process.VmSwap
	}
	return process, nil
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
