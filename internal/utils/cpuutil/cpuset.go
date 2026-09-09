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
	"cmp"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
)

// SystemCPUOnlinePath is the Linux sysfs path for online CPUs.
const SystemCPUOnlinePath = "/sys/devices/system/cpu/online"

// ParseOnlineCores returns the number of CPUs described by a Linux CPU list file.
func ParseOnlineCores(path string) (uint64, error) {
	v, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}

	return ParseCPUListCount(string(v))
}

// ParseCPUListCount returns the number of CPUs in a Linux CPU list.
func ParseCPUListCount(list string) (uint64, error) {
	list = strings.TrimSpace(list)
	if list == "" {
		return 0, nil
	}

	var count uint64
	for _, item := range strings.Split(list, ",") {
		if item == "" {
			return 0, fmt.Errorf("invalid CPU list %q", list)
		}

		first, last, err := parseCPURange(item)
		if err != nil {
			return 0, err
		}
		width := last - first
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

func parseCPURange(item string) (uint64, uint64, error) {
	firstText, lastText, isRange := strings.Cut(item, "-")
	first, err := strconv.ParseUint(firstText, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parse CPU %q: %w", item, err)
	}
	if !isRange {
		return first, first, nil
	}
	last, err := strconv.ParseUint(lastText, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parse CPU range %q: %w", item, err)
	}
	if last < first {
		return 0, 0, fmt.Errorf("invalid CPU range %q", item)
	}
	return first, last, nil
}

type cpuRange struct {
	first uint64
	last  uint64
}

// CPUListIntersectionCount counts CPUs common to all lists without expanding ranges.
func CPUListIntersectionCount(lists ...string) (uint64, error) {
	var common []cpuRange
	for index, list := range lists {
		ranges, err := parseCPURanges(list)
		if err != nil {
			return 0, err
		}
		if index == 0 {
			common = ranges
			continue
		}
		intersection := make([]cpuRange, 0, len(common)+len(ranges))
		for left, right := 0, 0; left < len(common) && right < len(ranges); {
			first := max(common[left].first, ranges[right].first)
			last := min(common[left].last, ranges[right].last)
			if first <= last {
				intersection = append(intersection, cpuRange{first: first, last: last})
			}
			if common[left].last < ranges[right].last {
				left++
			} else {
				right++
			}
		}
		common = intersection
	}
	var count uint64
	for _, interval := range common {
		width := interval.last - interval.first
		if width == math.MaxUint64 || count > math.MaxUint64-(width+1) {
			return 0, errors.New("cpu count overflow")
		}
		count += width + 1
	}
	return count, nil
}

func parseCPURanges(list string) ([]cpuRange, error) {
	list = strings.TrimSpace(list)
	if list == "" {
		return nil, nil
	}
	items := strings.Split(list, ",")
	ranges := make([]cpuRange, 0, len(items))
	for _, item := range items {
		if item == "" {
			return nil, fmt.Errorf("invalid CPU list %q", list)
		}
		first, last, err := parseCPURange(item)
		if err != nil {
			return nil, err
		}
		ranges = append(ranges, cpuRange{first: first, last: last})
	}
	slices.SortFunc(ranges, func(a, b cpuRange) int { return cmp.Compare(a.first, b.first) })
	merged := ranges[:1]
	for _, interval := range ranges[1:] {
		previous := &merged[len(merged)-1]
		if interval.first <= previous.last || (previous.last != math.MaxUint64 && interval.first == previous.last+1) {
			previous.last = max(previous.last, interval.last)
		} else {
			merged = append(merged, interval)
		}
	}
	return merged, nil
}

// MaxOnlineCPU returns the highest CPU ID in a Linux online CPU list.
func MaxOnlineCPU(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return -1
	}

	list := strings.TrimSpace(string(data))
	if list == "" {
		return -1
	}

	parts := strings.Split(list, ",")
	last := parts[len(parts)-1]
	if idx := strings.LastIndex(last, "-"); idx != -1 {
		last = last[idx+1:]
	}

	maxID, err := strconv.Atoi(last)
	if err != nil {
		return -1
	}
	return maxID
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
