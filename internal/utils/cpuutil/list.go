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
	"fmt"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
)

// MaxListLength bounds externally supplied CPU lists before parsing.
const MaxListLength = 65536

// Bound expansion independently of CPU IDs, which may be sparse.
const maxListEntries = 65536

type cpuRange struct {
	first uint64
	last  uint64
}

// Keep syntax parsing independent of selection limits and ID expansion.
func walkRanges(list string, visit func(cpuRange) error) error {
	list = strings.TrimSpace(list)
	if list == "" {
		return nil
	}
	for {
		item, rest, more := strings.Cut(list, ",")
		span, err := parseRange(item)
		if err != nil {
			return err
		}
		if err := visit(span); err != nil {
			return err
		}
		if !more {
			return nil
		}
		list = rest
	}
}

func parseRange(item string) (cpuRange, error) {
	firstText, lastText, isRange := strings.Cut(item, "-")
	first, err := parseCPU(firstText)
	if err != nil {
		return cpuRange{}, err
	}

	last := first
	if isRange {
		last, err = parseCPU(lastText)
		if err != nil {
			return cpuRange{}, err
		}
		if last < first {
			return cpuRange{}, fmt.Errorf("invalid CPU range %q: start exceeds end", item)
		}
	}

	return cpuRange{
		first: first,
		last:  last,
	}, nil
}

func parseCPU(value string) (uint64, error) {
	id, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid CPU ID %q: %w", value, err)
	}

	return id, nil
}

func parseBoundedRanges(list string) ([]cpuRange, error) {
	if len(list) > MaxListLength {
		return nil, fmt.Errorf("CPU list exceeds %d bytes", MaxListLength)
	}
	list = strings.TrimSpace(list)
	if list == "" {
		return nil, nil
	}
	ranges := make([]cpuRange, 0, strings.Count(list, ",")+1)
	var count uint64
	err := walkRanges(list, func(span cpuRange) error {
		if span.first > math.MaxInt32 || span.last > math.MaxInt32 {
			return fmt.Errorf("CPU ID %d must be between 0 and %d", max(span.first, span.last), math.MaxInt32)
		}
		width := span.last - span.first
		if width >= maxListEntries-count {
			return fmt.Errorf("CPU list expansion exceeds %d entries", maxListEntries)
		}
		count += width + 1
		ranges = append(ranges, span)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return mergeRanges(ranges), nil
}

func mergeRanges(ranges []cpuRange) []cpuRange {
	slices.SortFunc(ranges, func(a, b cpuRange) int { return cmp.Compare(a.first, b.first) })
	merged := ranges[:0]
	for _, span := range ranges {
		if len(merged) == 0 {
			merged = append(merged, span)
			continue
		}
		last := &merged[len(merged)-1]
		// Subtraction avoids overflow when an interval ends at the largest ID.
		if span.first > last.last && span.first-last.last > 1 {
			merged = append(merged, span)
		} else {
			last.last = max(last.last, span.last)
		}
	}

	return merged
}

// ParseList parses logical CPU IDs, sorts them and removes duplicates.
// An empty list means no CPU restriction.
func ParseList(list string) ([]int, error) {
	ranges, err := parseBoundedRanges(list)
	if err != nil {
		return nil, err
	}

	size := 0
	for _, span := range ranges {
		size += int(span.last - span.first + 1)
	}

	var ids []int
	if size != 0 {
		ids = make([]int, 0, size)
	}

	for _, span := range ranges {
		for id := span.first; id <= span.last; id++ {
			ids = append(ids, int(id))
		}
	}

	return ids, nil
}

// NormalizeList returns a sorted, deduplicated CPU list with adjacent IDs merged.
func NormalizeList(list string) (string, error) {
	ranges, err := parseBoundedRanges(list)
	if err != nil {
		return "", err
	}

	var output strings.Builder
	for i, span := range ranges {
		if i != 0 {
			output.WriteByte(',')
		}
		output.WriteString(strconv.FormatUint(span.first, 10))
		if span.first != span.last {
			output.WriteByte('-')
			output.WriteString(strconv.FormatUint(span.last, 10))
		}
	}

	return output.String(), nil
}

// ValidateOnline checks selected IDs against a Linux online CPU list file.
func ValidateOnline(ids []int, path string) error {
	if len(ids) == 0 {
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read online CPUs: %w", err)
	}

	online, err := parseBoundedRanges(string(data))
	if err != nil {
		return fmt.Errorf("parse online CPUs: %w", err)
	}

	for _, id := range ids {
		if id < 0 {
			return fmt.Errorf("CPU %d is not online on this node", id)
		}
		index, _ := slices.BinarySearchFunc(online, uint64(id), func(span cpuRange, id uint64) int {
			return cmp.Compare(span.last, id)
		})
		if index == len(online) || online[index].first > uint64(id) {
			return fmt.Errorf("CPU %d is not online on this node", id)
		}
	}

	return nil
}
