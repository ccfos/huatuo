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

// Package collector orchestrates runtime snapshots independently of their trigger.

package collector

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

func readProcessMemory(pid int) *memsnapshot.ProcessMemory {
	f, err := os.Open(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return &memsnapshot.ProcessMemory{Status: memsnapshot.StatusUnavailable, Reason: err.Error()}
	}
	defer f.Close()
	return parseProcessMemory(f)
}

func parseProcessMemory(r io.Reader) *memsnapshot.ProcessMemory {
	m := &memsnapshot.ProcessMemory{Status: memsnapshot.StatusComplete}
	fields := map[string]**uint64{
		"VmSize:": &m.VirtualBytes, "VmRSS:": &m.RSSBytes,
		"RssAnon:": &m.RSSAnonBytes, "RssFile:": &m.RSSFileBytes,
		"RssShmem:": &m.RSSShmemBytes, "VmSwap:": &m.SwapBytes,
		"VmPTE:": &m.PageTableBytes,
	}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		parts := strings.Fields(scanner.Text())
		if len(parts) == 0 {
			continue
		}
		dst, ok := fields[parts[0]]
		if !ok || len(parts) != 3 || parts[2] != "kB" {
			continue
		}
		value, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil || value > ^uint64(0)/1024 {
			continue
		}
		value *= 1024
		*dst = &value
	}
	available := 0
	for _, value := range fields {
		if *value != nil {
			available++
		}
	}
	if available != len(fields) {
		m.Status, m.Reason = memsnapshot.StatusPartial, "process memory fields are missing or invalid"
		if available == 0 {
			m.Status = memsnapshot.StatusUnavailable
		}
	}
	if err := scanner.Err(); err != nil {
		m.Status, m.Reason = memsnapshot.StatusPartial, "read process status: "+err.Error()
		if available == 0 {
			m.Status = memsnapshot.StatusUnavailable
		}
	}
	return m
}
