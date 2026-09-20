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
	"encoding/binary"
	"fmt"
	"sort"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

const (
	maxSampleBytes          = 32 << 20
	windowBytes             = 4 << 10
	maxKlassAttempts        = 1024
	maxBatchKlassCandidates = 4096
)

type sampleWindow struct {
	start     uint64
	regionTop uint64
	raw       []byte
	size      uint64
}

func scanKnownWindow(sample sampleWindow, classes map[uint64]*klass,
	encoding ptrEncoding,
	metadata *vmMeta, mirrorOopSizeOffset int,
	observations map[uint64]classSample,
) {
	raw := sample.raw
	alignment := metadata.alignment()
	for offset := uint64(0); offset+encoding.headerBytes() <= uint64(len(raw)); offset += alignment {
		mark := binary.LittleEndian.Uint64(raw[offset : offset+8])
		if !scannableObjectMark(mark) {
			continue
		}
		klassAddress, validKlass := encoding.klassAddress(raw[offset:])
		if !validKlass {
			continue
		}
		klass := classes[klassAddress]
		if klass == nil {
			continue
		}
		objectBytes, err := objectSize(raw[offset:], klass, metadata,
			mirrorOopSizeOffset, encoding.headerBytes())
		if err != nil || objectBytes == 0 || objectBytes > maxJavaObjectBytes {
			continue
		}
		if sample.start > sample.regionTop ||
			offset > sample.regionTop-sample.start {
			continue
		}
		objectAddress := sample.start + offset
		if objectBytes > sample.regionTop-objectAddress {
			continue
		}
		addClassSample(observations, klassAddress, 1, objectBytes)
		offset += objectBytes - alignment
	}
}

// A live object may be unlocked (01), lightweight-locked (00), or have an
// inflated monitor (10). The marked/forwarded state (11) is not stable enough
// for an external concurrent scan. Candidate discovery remains stricter and
// only trusts unlocked headers; the locked states are accepted only after the
// Klass has already been validated.
func scannableObjectMark(mark uint64) bool {
	return mark&3 != 3
}

func resolveBatchKlasses(memory processMemory, metadata *vmMeta,
	batch []sampleWindow, classes map[uint64]*klass,
	attempted map[uint64]struct{}, encoding ptrEncoding, limit int,
) (int, bool) {
	if err := memory.check(); err != nil {
		return 0, false
	}
	if limit <= 0 {
		return 0, true
	}
	alignment := metadata.alignment()
	if alignment == 0 {
		alignment = defaultObjectAlignment
	}
	hits := make(map[uint64]uint16)
	for _, sample := range batch {
		raw := sample.raw
		for offset := uint64(0); offset+encoding.headerBytes() <= uint64(len(raw)); offset += alignment {
			mark := binary.LittleEndian.Uint64(raw[offset : offset+8])
			if mark&3 != 1 {
				continue
			}
			address, valid := encoding.klassAddress(raw[offset:])
			if !valid || classes[address] != nil ||
				metadata.image == nil || !metadata.image.contains(address, 8) {
				continue
			}
			if _, seen := attempted[address]; seen {
				continue
			}
			if count, exists := hits[address]; exists {
				if count != ^uint16(0) {
					hits[address] = count + 1
				}
			} else if len(hits) < maxBatchKlassCandidates {
				hits[address] = 1
			}
		}
	}
	candidates := make([]uint64, 0, len(hits))
	for address := range hits {
		candidates = append(candidates, address)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if hits[candidates[i]] == hits[candidates[j]] {
			return candidates[i] < candidates[j]
		}
		return hits[candidates[i]] > hits[candidates[j]]
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	for _, address := range candidates {
		attempted[address] = struct{}{}
	}
	for address, class := range readKlassBatch(memory, metadata, candidates) {
		if !metadata.cacheKlass(classes, address, class) {
			return len(candidates), false
		}
	}
	return len(candidates), memory.check() == nil
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

// readBatchEnd bounds each fixed-size sample read to the process_vm_readv IOV
// limit. The production 4 KiB windows stay below maxReadBytes at this limit.
func readBatchEnd(begin, total int) int {
	end := begin + maxReadIOVs
	if end > total {
		end = total
	}
	return end
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

// scanWindows reads one bounded process_vm_readv batch and
// hands it to the classifier before issuing the next batch. At most one batch
// of victim heap bytes is retained at a time.
func scanWindows(memory processMemory, regions []region,
	seed, budget, windowBytes uint64,
	visit func([]sampleWindow) bool,
) string {
	windows := planWindows(regions, seed, budget, windowBytes)
	skipped := 0
	var firstReadErr error
	for begin := 0; begin < len(windows); {
		if err := memory.check(); err != nil {
			return fmt.Sprintf(
				"used-byte-weighted HotSpot sampling stopped: %v", err,
			)
		}
		end := readBatchEnd(begin, len(windows))
		if end <= begin {
			return "invalid used-byte-weighted HotSpot sample batch"
		}
		batch := windows[begin:end]
		var batchBytes int
		for index := range batch {
			batchBytes += int(batch[index].size)
		}
		buffer := make([]byte, batchBytes)
		local := make([]unix.Iovec, 0, len(batch))
		remote := make([]unix.RemoteIovec, 0, len(batch))
		offset := 0
		for index := range batch {
			length := int(batch[index].size)
			batch[index].raw = buffer[offset : offset+length]
			local = append(local, unix.Iovec{
				Base: &batch[index].raw[0], Len: uint64(length),
			})
			remote = append(remote, unix.RemoteIovec{
				Base: uintptr(batch[index].start), Len: length,
			})
			offset += length
		}
		if err := memory.check(); err != nil {
			return fmt.Sprintf(
				"used-byte-weighted HotSpot sampling stopped before process_vm_readv: %v",
				err,
			)
		}
		read, readErr := unix.ProcessVMReadv(memory.pid, local, remote, 0)
		if err := memory.check(); err != nil {
			return fmt.Sprintf(
				"used-byte-weighted HotSpot sampling stopped after process_vm_readv: %v",
				err,
			)
		}
		remaining := read
		completed := 0
		for index := range batch {
			length := len(batch[index].raw)
			if remaining < length {
				break
			}
			remaining -= length
			completed++
		}
		if completed != 0 && !visit(batch[:completed]) {
			return "deadline reached during used-byte-weighted HotSpot sampling"
		}
		if completed != len(batch) {
			if firstReadErr == nil {
				if readErr != nil {
					firstReadErr = readErr
				} else {
					firstReadErr = fmt.Errorf("short read: got %d of %d bytes",
						read, batchBytes)
				}
			}
			skipped += len(batch) - completed
		}
		for index := range batch {
			batch[index].raw = nil
		}
		begin = end
	}
	if skipped != 0 {
		return fmt.Sprintf("skipped %d windows after sample batch reads failed: %v",
			skipped, firstReadErr)
	}
	return ""
}
