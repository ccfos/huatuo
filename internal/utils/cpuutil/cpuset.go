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
	"math"
	"os"
)

// SystemCPUOnlinePath is the Linux sysfs path for online CPUs.
const SystemCPUOnlinePath = "/sys/devices/system/cpu/online"

// ReadListCount returns the number of CPUs described by a Linux CPU list file.
// Overlapping entries retain their individual counts.
func ReadListCount(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}

	var count uint64
	err = walkRanges(string(data), func(span cpuRange) error {
		width := span.last - span.first
		if width == math.MaxUint64 {
			return errors.New("cpu count overflow")
		}
		size := width + 1
		if count > math.MaxUint64-size {
			return errors.New("cpu count overflow")
		}
		count += size
		return nil
	})
	if err != nil {
		return 0, err
	}

	return count, nil
}

// ReadListMaxID returns the largest CPU ID in a Linux CPU list file.
// It returns -1 for unreadable, empty or invalid files, or IDs that do not fit int.
func ReadListMaxID(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return -1
	}

	var maxID uint64
	found := false
	err = walkRanges(string(data), func(span cpuRange) error {
		maxID = max(maxID, span.last)
		found = true
		return nil
	})
	if err != nil || !found || maxID > uint64(math.MaxInt) {
		return -1
	}

	return int(maxID)
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
