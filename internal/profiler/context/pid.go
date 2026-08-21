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

package context

import (
	"fmt"
	"strconv"
	"strings"
)

// ParsePIDs parses a comma-separated list of positive process IDs.
func ParsePIDs(value string) ([]int, error) {
	if value == "" {
		return nil, nil
	}

	parts := strings.Split(value, ",")
	pids := make([]int, 0, len(parts))
	seen := make(map[int]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		pid, err := strconv.Atoi(part)
		if err != nil || pid <= 0 {
			return nil, fmt.Errorf("invalid --pid value %q: expected positive comma-separated PIDs", value)
		}
		if _, ok := seen[pid]; ok {
			return nil, fmt.Errorf("invalid --pid value %q: PID %d is duplicated", value, pid)
		}

		seen[pid] = struct{}{}
		pids = append(pids, pid)
	}

	return pids, nil
}

// PID returns the first target PID for profilers that support one process.
func (pctx *ProfilerContext) PID() int {
	if len(pctx.PIDs) == 0 {
		return 0
	}
	return pctx.PIDs[0]
}
