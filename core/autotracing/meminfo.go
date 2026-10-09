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
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

// readMemInfo returns the requested values from /proc/meminfo.
func readMemInfo(requiredKeys map[string]bool) (map[string]int, error) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readMemInfoFrom(file, requiredKeys)
}

func readMemInfoFrom(reader io.Reader, requiredKeys map[string]bool) (map[string]int, error) {
	results := make(map[string]int)
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		if _, ok := requiredKeys[key]; !ok {
			continue
		}
		value, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", key, err)
		}
		results[key] = value
		if len(results) == len(requiredKeys) {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(results) != len(requiredKeys) {
		missing := make([]string, 0, len(requiredKeys)-len(results))
		for key := range requiredKeys {
			if _, ok := results[key]; !ok {
				missing = append(missing, key)
			}
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("missing required memory info keys: %s", strings.Join(missing, ", "))
	}
	return results, nil
}
