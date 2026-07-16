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

package provider

import "fmt"

func validatePythonToolPath(toolPath string) error {
	return validateToolFile("Python", toolPath, "py-spy", true)
}

func validatePythonAggregationWindow(duration, interval int) error {
	if duration != interval {
		return fmt.Errorf(
			"Python CPU profiler does not support continuous profiling: aggregation interval (%ds) must equal duration (%ds)",
			interval,
			duration,
		)
	}
	return nil
}

func pythonRootPids(pids []int, parentPID func(int) (int, error)) ([]int, error) {
	targets := make(map[int]struct{}, len(pids))
	for _, pid := range pids {
		targets[pid] = struct{}{}
	}

	roots := make([]int, 0, len(pids))
	for _, pid := range pids {
		ancestor := pid
		seen := map[int]struct{}{pid: {}}
		isDescendant := false
		for ancestor > 1 {
			ppid, err := parentPID(ancestor)
			if err != nil {
				return nil, fmt.Errorf("resolve Python target PID %d ancestry: %w", pid, err)
			}
			if ppid <= 1 {
				break
			}
			if _, ok := seen[ppid]; ok {
				return nil, fmt.Errorf("resolve Python target PID %d ancestry: process parent cycle at PID %d", pid, ppid)
			}
			if _, ok := targets[ppid]; ok {
				isDescendant = true
				break
			}
			seen[ppid] = struct{}{}
			ancestor = ppid
		}
		if !isDescendant {
			roots = append(roots, pid)
		}
	}
	return roots, nil
}
