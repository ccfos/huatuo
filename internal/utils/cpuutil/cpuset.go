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

package cpuutil

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
)

const hostOnlineCPUPath = "/sys/devices/system/cpu/online"

// HostOnlineCPUCount returns the number of online logical CPUs on the host.
func HostOnlineCPUCount() (uint64, error) {
	return CPUSetCount(hostOnlineCPUPath)
}

// CPUSetCount returns the number of CPUs described by a Linux CPU list file.
func CPUSetCount(path string) (uint64, error) {
	v, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}

	var count uint64
	for _, item := range strings.Split(strings.TrimSpace(string(v)), ",") {
		if item == "" {
			return 0, fmt.Errorf("invalid CPU list %q", string(v))
		}

		bounds := strings.Split(item, "-")
		switch len(bounds) {
		case 1:
			if _, err := strconv.ParseUint(bounds[0], 10, 64); err != nil {
				return 0, fmt.Errorf("parse CPU %q: %w", item, err)
			}
			count++
		case 2:
			first, err := strconv.ParseUint(bounds[0], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("parse CPU range %q: %w", item, err)
			}
			last, err := strconv.ParseUint(bounds[1], 10, 64)
			if err != nil || last < first {
				return 0, fmt.Errorf("invalid CPU range %q", item)
			}
			width := last - first
			if count > math.MaxUint64-width-1 {
				return 0, fmt.Errorf("CPU list count overflow")
			}
			count += width + 1
		default:
			return 0, fmt.Errorf("invalid CPU range %q", item)
		}
	}

	if count == 0 {
		return 0, fmt.Errorf("empty CPU list")
	}
	return count, nil
}

// CPUCapacity returns the effective CPU capacity after applying quota and cpuset.
func CPUCapacity(quota, period, effective, fallback uint64) (float64, error) {
	if effective == 0 {
		effective = fallback
	}
	if effective == 0 {
		return 0, fmt.Errorf("container effective CPU count must be positive")
	}
	if quota == 0 {
		return 0, fmt.Errorf("container cpu quota must be positive")
	}
	if quota == math.MaxUint64 {
		return float64(effective), nil
	}
	if period == 0 {
		return 0, fmt.Errorf("container cpu period must be positive")
	}
	return min(float64(effective), float64(quota)/float64(period)), nil
}
