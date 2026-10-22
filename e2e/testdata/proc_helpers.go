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
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Keep the reference independent of the collector's process and cgroup helpers.
func processCPUTime(pid int, ticks float64) (float64, float64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, 0, err
	}
	user, system, err := parseProcessCPUTime(data, ticks)
	if err != nil {
		return 0, 0, fmt.Errorf("worker %d: %w", pid, err)
	}
	return user, system, nil
}

func parseProcessCPUTime(data []byte, ticks float64) (float64, float64, error) {
	fields := strings.Fields(string(data[bytes.LastIndexByte(data, ')')+1:]))
	if len(fields) < 13 {
		return 0, 0, fmt.Errorf("invalid process stat: missing CPU fields")
	}
	user, err := strconv.ParseFloat(fields[11], 64)
	if err != nil {
		return 0, 0, err
	}
	system, err := strconv.ParseFloat(fields[12], 64)
	if err != nil {
		return 0, 0, err
	}
	return user / ticks, system / ticks, nil
}
