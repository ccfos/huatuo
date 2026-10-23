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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cilium/ebpf"
)

type counters struct {
	Voluntary   uint64    `json:"Voluntary"`
	Involuntary uint64    `json:"Involuntary"`
	Zones       [4]uint64 `json:"Zones"`
}

type target struct {
	Pod       string   `json:"Pod"`
	Container string   `json:"Container"`
	Namespace string   `json:"Namespace"`
	CSS       string   `json:"CSS"`
	TIDs      []uint32 `json:"TIDs"`
}

type snapshot struct {
	MapIDs        map[string]ebpf.MapID     `json:"MapIDs"`
	ProgramIDs    map[string]ebpf.ProgramID `json:"ProgramIDs"`
	Host          counters                  `json:"Host"`
	Containers    map[string]counters       `json:"Containers"`
	ContainerCPUs map[string][]counters     `json:"ContainerCPUs"`
}

var mapTypes = map[string]ebpf.MapType{
	"latency": ebpf.Hash, "cpu_tg_metric": ebpf.PerCPUHash, "cpu_host_metric": ebpf.PerCPUArray,
}

var programTypes = map[string]ebpf.ProgramType{
	"sched_wakeup_new_entry":      ebpf.TracePoint,
	"sched_wakeup_entry":          ebpf.TracePoint,
	"sched_switch_entry":          ebpf.RawTracepoint,
	"sched_process_exit_entry":    ebpf.RawTracepoint,
	"free_fair_sched_group_entry": ebpf.Kprobe,
}

func fdIDs(data string) (uint32, uint32, error) {
	var mapID, programID uint32
	for _, line := range strings.Split(data, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || (key != "map_id" && key != "prog_id") {
			continue
		}
		id, err := strconv.ParseUint(strings.TrimSpace(value), 10, 32)
		if err != nil || id == 0 {
			return 0, 0, fmt.Errorf("invalid BPF fdinfo field %q", line)
		}
		if key == "map_id" {
			mapID = uint32(id)
		} else {
			programID = uint32(id)
		}
	}
	return mapID, programID, nil
}

type objects struct {
	maps     map[string]*ebpf.Map
	snapshot snapshot
}

func (o *objects) close() {
	for _, m := range o.maps {
		m.Close()
	}
}

func discover(pid int) (_ *objects, retErr error) {
	entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/fdinfo", pid))
	if err != nil {
		return nil, fmt.Errorf("read bamai BPF descriptors: %w", err)
	}
	o := &objects{maps: make(map[string]*ebpf.Map), snapshot: snapshot{
		MapIDs: make(map[string]ebpf.MapID), ProgramIDs: make(map[string]ebpf.ProgramID),
	}}
	defer func() {
		if retErr != nil {
			o.close()
		}
	}()
	seenMaps, seenPrograms := make(map[uint32]bool), make(map[uint32]bool)
	for _, entry := range entries {
		path := filepath.Join(fmt.Sprintf("/proc/%d/fdinfo", pid), entry.Name())
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue // Other daemon subsystems may close unrelated descriptors.
		}
		if err != nil {
			return nil, err
		}
		mapID, programID, err := fdIDs(string(data))
		if err != nil {
			return nil, err
		}
		if mapID != 0 && !seenMaps[mapID] {
			seenMaps[mapID] = true
			m, err := ebpf.NewMapFromID(ebpf.MapID(mapID))
			if err != nil {
				return nil, fmt.Errorf("open bamai map %d: %w", mapID, err)
			}
			info, err := m.Info()
			if err != nil {
				m.Close()
				return nil, err
			}
			if _, wanted := mapTypes[info.Name]; !wanted {
				m.Close()
				continue
			}
			if _, exists := o.maps[info.Name]; exists {
				m.Close()
				return nil, fmt.Errorf("bamai PID %d owns duplicate map %s", pid, info.Name)
			}
			o.maps[info.Name] = m
			o.snapshot.MapIDs[info.Name] = ebpf.MapID(mapID)
		}
		if programID != 0 && !seenPrograms[programID] {
			seenPrograms[programID] = true
			p, err := ebpf.NewProgramFromID(ebpf.ProgramID(programID))
			if err != nil {
				return nil, err
			}
			info, err := p.Info()
			p.Close()
			if err != nil {
				return nil, err
			}
			for name, typ := range programTypes {
				// Kernel BPF names contain at most 15 characters.
				if info.Name == name[:15] {
					if info.Type != typ {
						return nil, fmt.Errorf("program %s type=%s, want %s", name, info.Type, typ)
					}
					o.snapshot.ProgramIDs[name] = ebpf.ProgramID(programID)
				}
			}
		}
	}
	return o, nil
}

func (o *objects) validate() error {
	for name, typ := range mapTypes {
		m := o.maps[name]
		if m == nil {
			return fmt.Errorf("runqlat map %s is missing from bamai", name)
		}
		keySize, valueSize, capacity := uint32(8), uint32(48), uint32(10000)
		if name == "latency" {
			keySize, valueSize = 4, 8
		} else if name == "cpu_host_metric" {
			keySize, capacity = 4, 1
		}
		info, err := m.Info()
		if err != nil {
			return err
		}
		if info.Type != typ || info.KeySize != keySize || info.ValueSize != valueSize || info.MaxEntries != capacity {
			return fmt.Errorf("map %s has unexpected type/size/capacity: %+v", name, info)
		}
	}
	for name := range programTypes {
		if o.snapshot.ProgramIDs[name] == 0 {
			return fmt.Errorf("runqlat program %s is missing from bamai", name)
		}
	}
	return nil
}

func sum(values []counters) counters {
	var total counters
	for _, value := range values {
		total.Voluntary += value.Voluntary
		total.Involuntary += value.Involuntary
		for zone, n := range value.Zones {
			total.Zones[zone] += n
		}
	}
	return total
}

func cssKey(t *target) (uint64, error) {
	key, err := strconv.ParseUint(strings.TrimPrefix(t.CSS, "0x"), 16, 64)
	if err != nil || key == 0 {
		return 0, fmt.Errorf("Pod %s has invalid CPU CSS %q", t.Pod, t.CSS)
	}
	return key, nil
}

func (o *objects) read(targets []target) (snapshot, error) {
	s := o.snapshot
	var values []counters
	if err := o.maps["cpu_host_metric"].Lookup(uint32(0), &values); err != nil {
		return s, err
	}
	s.Host = sum(values)
	s.Containers = make(map[string]counters, len(targets))
	s.ContainerCPUs = make(map[string][]counters, len(targets))
	for i := range targets {
		t := &targets[i]
		key, err := cssKey(t)
		if err != nil {
			return s, err
		}
		if err := o.maps["cpu_tg_metric"].Lookup(key, &values); err != nil {
			return s, fmt.Errorf("Pod %s CPU CSS %s: %w", t.Pod, t.CSS, err)
		}
		s.Containers[t.Pod] = sum(values)
		s.ContainerCPUs[t.Pod] = values
	}
	return s, nil
}
