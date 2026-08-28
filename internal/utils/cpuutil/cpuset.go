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
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
)

// SystemCPUOnlinePath is the Linux sysfs path for online CPUs.
const SystemCPUOnlinePath = "/sys/devices/system/cpu/online"

type cpuRange struct {
	first uint64
	last  uint64
}

func parseCPURanges(path string) ([]cpuRange, error) {
	v, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	list := strings.TrimSpace(string(v))
	if list == "" {
		return nil, nil
	}

	ranges := make([]cpuRange, 0, strings.Count(list, ",")+1)
	for _, item := range strings.Split(list, ",") {
		if item == "" {
			return nil, fmt.Errorf("invalid CPU list %q", list)
		}

		firstText, lastText, isRange := strings.Cut(item, "-")
		first, err := strconv.ParseUint(firstText, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse CPU %q: %w", item, err)
		}

		last := first
		if isRange {
			last, err = strconv.ParseUint(lastText, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("parse CPU range %q: %w", item, err)
			}
			if last < first {
				return nil, fmt.Errorf("invalid CPU range %q", item)
			}
		}
		ranges = append(ranges, cpuRange{first: first, last: last})
	}

	return ranges, nil
}

// ParseOnlineCores returns the number of CPUs described by a Linux CPU list file.
func ParseOnlineCores(path string) (uint64, error) {
	ranges, err := parseCPURanges(path)
	if err != nil {
		return 0, err
	}

	var count uint64
	for _, r := range ranges {
		width := r.last - r.first
		if width == math.MaxUint64 {
			return 0, errors.New("cpu count overflow")
		}
		size := width + 1
		if count > math.MaxUint64-size {
			return 0, errors.New("cpu count overflow")
		}
		count += size
	}

	return count, nil
}

// ParseOnlineCPUSet returns the CPU IDs described by a Linux online CPU list.
func ParseOnlineCPUSet(path string, possible int) (map[int]struct{}, error) {
	if possible <= 0 {
		return nil, errors.New("possible CPU count must be positive")
	}

	ranges, err := parseCPURanges(path)
	if err != nil {
		return nil, err
	}
	if len(ranges) == 0 {
		return nil, errors.New("online CPU list is empty")
	}

	online := make(map[int]struct{})
	for _, r := range ranges {
		if r.last >= uint64(possible) {
			return nil, fmt.Errorf(
				"online CPU range %d-%d exceeds possible CPU IDs 0-%d",
				r.first,
				r.last,
				possible-1,
			)
		}
		for cpu := r.first; cpu <= r.last; cpu++ {
			online[int(cpu)] = struct{}{}
		}
	}

	return online, nil
}

// BoundCores returns the effective CPU capacity after applying quota and cpuset.
func BoundCores(quota, period, effective, fallback uint64) (float64, error) {
	if effective == 0 {
		effective = fallback
	}
	switch {
	case effective == 0:
		return 0, errors.New("effective cpu count is zero")
	case quota == 0:
		return 0, errors.New("cpu quota is zero")
	case quota == math.MaxUint64:
		return float64(effective), nil
	case period == 0:
		return 0, errors.New("cpu period is zero")
	}
	return min(float64(effective), float64(quota)/float64(period)), nil
}
