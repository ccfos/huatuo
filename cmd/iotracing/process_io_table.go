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

package main

import "sort"

// fileEntry pairs a BPF file-IO record with its sortable size.
type fileEntry struct {
	Record *bpfFilesystemIO
	Size   uint64
}

// pidGroup is one pid's file-IO records and their summed Size.
type pidGroup struct {
	Pid   uint32
	Files []*fileEntry
	Total uint64
}

// ProcessIOTable groups IO records by pid for top-N ranking.
type ProcessIOTable map[uint32]*pidGroup

func (t ProcessIOTable) Add(pid uint32, e *fileEntry) {
	g := t[pid]
	if g == nil {
		g = &pidGroup{Pid: pid}
		t[pid] = g
	}

	g.Files = append(g.Files, e)
	g.Total += e.Size
}

// TopN returns up to n groups ordered by descending Total; each
// returned group's Files is sorted descending by Size in place.
func (t ProcessIOTable) TopN(n int) []*pidGroup {
	groups := make([]*pidGroup, 0, len(t))
	for _, g := range t {
		groups = append(groups, g)
	}

	sort.Slice(groups, func(i, j int) bool { return groups[i].Total > groups[j].Total })

	if n < len(groups) {
		groups = groups[:n]
	}

	for _, g := range groups {
		sort.Slice(g.Files, func(i, j int) bool { return g.Files[i].Size > g.Files[j].Size })
	}

	return groups
}
