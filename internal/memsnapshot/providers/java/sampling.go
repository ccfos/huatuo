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

package java

import (
	"sort"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

type sampleWindow struct {
	start     uint64
	regionTop uint64
	raw       []byte
	size      uint64
}

func coprimeStride(size int, seed uint64) int {
	if size <= 1 {
		return 1
	}
	stride := int(seed%uint64(size-1)) + 1
	for gcd(stride, size) != 1 {
		stride++
		if stride >= size {
			stride = 1
		}
	}
	return stride
}

func gcd(left, right int) int {
	for right != 0 {
		left, right = right, left%right
	}
	return left
}

func mixSampleSeed(value uint64) uint64 {
	value ^= value >> 30
	value *= 0xbf58476d1ce4e5b9
	value ^= value >> 27
	value *= 0x94d049bb133111eb
	return value ^ (value >> 31)
}

// planWindows samples the concatenated ordinary used-byte ranges. Systematic
// byte positions make a short tail window proportional to its real size rather
// than giving it the same weight as a full window. A full budget still returns
// every unique window exactly once.
func planWindows(regions []region, seed, budget,
	windowBytes uint64,
) []sampleWindow {
	if budget == 0 || windowBytes == 0 {
		return nil
	}
	type weightedRegion struct {
		region  region
		byteEnd uint64
	}
	weighted := make([]weightedRegion, 0, len(regions))
	var totalUsed, totalSlots uint64
	for _, region := range regions {
		if region.bottom == 0 || region.top <= region.bottom {
			continue
		}
		used := region.top - region.bottom
		slots := used / windowBytes
		if used%windowBytes != 0 {
			slots++
		}
		totalUsed = memsnapshot.SaturatingAdd(totalUsed, used)
		totalSlots = memsnapshot.SaturatingAdd(totalSlots, slots)
		weighted = append(weighted, weightedRegion{
			region: region, byteEnd: totalUsed,
		})
	}
	if totalUsed == 0 || totalSlots == 0 {
		return nil
	}
	windowAt := func(point uint64) sampleWindow {
		regionIndex := sort.Search(len(weighted), func(index int) bool {
			return weighted[index].byteEnd > point
		})
		bytesBefore := uint64(0)
		if regionIndex != 0 {
			bytesBefore = weighted[regionIndex-1].byteEnd
		}
		region := weighted[regionIndex].region
		offset := (point - bytesBefore) / windowBytes * windowBytes
		return sampleWindow{
			start:     region.bottom + offset,
			regionTop: region.top,
			size:      min(windowBytes, region.top-region.bottom-offset),
		}
	}
	appendAll := func(result []sampleWindow) []sampleWindow {
		for _, item := range weighted {
			used := item.region.top - item.region.bottom
			for offset := uint64(0); offset < used; offset += windowBytes {
				start := item.region.bottom + offset
				result = append(result, sampleWindow{
					start: start, regionTop: item.region.top,
					size: min(windowBytes, used-offset),
				})
			}
		}
		return result
	}
	windowCount := totalSlots
	if budget < totalUsed {
		windowCount = budget / windowBytes
		if windowCount == 0 {
			windowCount = 1
		}
		if windowCount > totalSlots {
			windowCount = totalSlots
		}
	}
	result := make([]sampleWindow, 0, int(windowCount))
	if budget >= totalUsed {
		result = appendAll(result)
	} else {
		// Partial budgets are window-aligned, so adjacent systematic points
		// are at least one window apart and cannot select the same slot.
		base, remainder := totalUsed/windowCount, totalUsed%windowCount
		random := mixSampleSeed(seed) % totalUsed
		randomBase, randomRemainder := random/windowCount, random%windowCount
		for sequence := uint64(0); sequence < windowCount; sequence++ {
			point := sequence*base + randomBase +
				(sequence*remainder+randomRemainder)/windowCount
			result = append(result, windowAt(point))
		}
	}
	if len(result) <= 1 {
		return result
	}
	ordered := make([]sampleWindow, len(result))
	start := int(mixSampleSeed(seed^0x94d049bb133111eb) % uint64(len(result)))
	stride := coprimeStride(len(result),
		mixSampleSeed(seed^0x9e3779b97f4a7c15))
	for index := range ordered {
		ordered[index] = result[(start+index*stride)%len(result)]
	}
	return ordered
}
