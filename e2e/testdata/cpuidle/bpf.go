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

package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cilium/ebpf"
)

type perfObjects struct {
	PID       int            `json:"pid"`
	MapID     ebpf.MapID     `json:"map_id"`
	ProgramID ebpf.ProgramID `json:"program_id"`
	Samples   uint64         `json:"samples"`
}

func childPIDs(pid int) ([]int, error) {
	root := fmt.Sprintf("/proc/%d/task", pid)
	threads, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var children []int
	// os/exec can launch perf from any Go runtime thread, not just the leader.
	for _, thread := range threads {
		data, err := os.ReadFile(filepath.Join(root, thread.Name(), "children"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, field := range strings.Fields(string(data)) {
			child, err := strconv.Atoi(field)
			if err != nil {
				return nil, err
			}
			children = append(children, child)
		}
	}
	return children, nil
}

func inspectPerf(pid, targetPID int, comm string, duration time.Duration, output string) error {
	deadline := time.Now().Add(duration)
	lastErr := errors.New("perf subprocess has not started")
	for time.Now().Before(deadline) {
		children, err := childPIDs(pid)
		if err != nil {
			return err
		}
		for _, child := range children {
			exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", child))
			if err != nil || filepath.Base(exe) != "perf" {
				continue
			}
			objects, err := readPerfObjects(child, targetPID, comm)
			if err != nil {
				lastErr = err
				continue
			}
			data, err := json.Marshal(objects)
			if err != nil {
				return err
			}
			return os.WriteFile(output, data, 0o600)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("no target samples from bamai PID %d's perf child within %s: %w", pid, duration, lastErr)
}

func readPerfObjects(pid, targetPID int, comm string) (*perfObjects, error) {
	root := fmt.Sprintf("/proc/%d/fdinfo", pid)
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	objects := &perfObjects{PID: pid}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(data), "\n") {
			key, value, ok := strings.Cut(line, ":")
			if !ok || (key != "map_id" && key != "prog_id") {
				continue
			}
			id, err := strconv.ParseUint(strings.TrimSpace(value), 10, 32)
			if err != nil {
				return nil, err
			}
			if key == "map_id" {
				m, err := ebpf.NewMapFromID(ebpf.MapID(id))
				if err != nil {
					return nil, err
				}
				info, err := m.Info()
				if err == nil && info.Name == "counts" {
					objects.MapID = ebpf.MapID(id)
					objects.Samples, err = readPerfSamples(m, info, targetPID, comm)
				}
				m.Close()
				if err != nil {
					return nil, err
				}
			} else {
				p, err := ebpf.NewProgramFromID(ebpf.ProgramID(id))
				if err != nil {
					return nil, err
				}
				info, err := p.Info()
				p.Close()
				if err != nil {
					return nil, err
				}
				if info.Name == "perf_event_sw_cpu_clock"[:15] && info.Type == ebpf.PerfEvent {
					objects.ProgramID = ebpf.ProgramID(id)
				}
			}
		}
	}
	if objects.MapID == 0 || objects.ProgramID == 0 || objects.Samples == 0 {
		return nil, fmt.Errorf("perf PID %d: program/map/target samples not ready: %+v", pid, objects)
	}
	return objects, nil
}

func readPerfSamples(m *ebpf.Map, info *ebpf.MapInfo, pid int, comm string) (uint64, error) {
	if info.Type != ebpf.Hash || info.KeySize != 360 || info.ValueSize != 8 || info.MaxEntries != 1024 {
		return 0, fmt.Errorf("perf counts map has unexpected ABI: %+v", info)
	}
	var key [360]byte
	var value, samples uint64
	iter := m.Iterate()
	for iter.Next(&key, &value) {
		// Two 20-address stacks and their sizes precede TGID and comm in key_t.
		tgid := binary.LittleEndian.Uint32(key[336:340])
		name := strings.TrimRight(string(key[340:356]), "\x00")
		if tgid != uint32(pid) || name != comm {
			return 0, fmt.Errorf("perf captured unrelated task pid=%d comm=%q, want pid=%d comm=%q", tgid, name, pid, comm)
		}
		samples += value
	}
	return samples, iter.Err()
}

func checkReleased(objects *perfObjects) error {
	if objects.MapID == 0 || objects.ProgramID == 0 {
		return errors.New("BPF release check requires recorded map and program IDs")
	}
	m, err := ebpf.NewMapFromID(objects.MapID)
	if err == nil {
		m.Close()
		return fmt.Errorf("perf counts map ID %d remains loaded", objects.MapID)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	p, err := ebpf.NewProgramFromID(objects.ProgramID)
	if err == nil {
		p.Close()
		return fmt.Errorf("perf program ID %d remains loaded", objects.ProgramID)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
