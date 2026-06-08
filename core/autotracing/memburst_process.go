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

package autotracing

import (
	"sort"

	"github.com/shirou/gopsutil/process"
)

type processMemInfo struct {
	PID         int32
	ProcessName string
	MemSize     uint64
}

type memoryType int

const (
	memoryRSS    memoryType = iota // same as VmRSS in /proc/pid/status
	memoryShared                   // same as RssFile+RssShmem in /proc/pid/status
)

// topMemoryProcesses returns the top N processes consuming the most memory
// (For example: memory, resident shared pages) which is assigned by MemoryMetric.
func topMemoryProcesses(topN int, metric memoryType) ([]*processMemInfo, error) {
	procs, err := process.Processes()
	if err != nil {
		return nil, err
	}

	var infos []*processMemInfo
	for _, p := range procs {
		var val uint64
		switch metric {
		case memoryRSS:
			mem, err := p.MemoryInfo()
			if err != nil {
				continue
			}
			val = mem.RSS
		case memoryShared:
			mem, err := p.MemoryInfoEx()
			if err != nil {
				continue
			}
			val = mem.Shared
		}

		name, err := p.Name()
		if err != nil {
			continue
		}

		infos = append(infos, &processMemInfo{
			PID:         p.Pid,
			ProcessName: name,
			MemSize:     val,
		})
	}

	// descending sort by MemSize
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].MemSize > infos[j].MemSize
	})

	if len(infos) < topN {
		return infos, nil
	}
	return infos[:topN], nil
}
