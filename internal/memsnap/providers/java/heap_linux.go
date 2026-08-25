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
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/memsnap"
)

const (
	maxSampleBytes          = 32 << 20
	windowBytes             = 4 << 10
	maxKlassAttempts        = 1024
	maxBatchKlassCandidates = 4096
	maxG1Regions            = 1 << 18
)

type region struct {
	bottom   uint64
	top      uint64
	tag      uint32
	hasTag   bool
	capacity uint64
}

type regionGroup struct {
	regions []region
}

type heapRegions struct {
	ordinary     []region
	humongous    []regionGroup
	ordinaryUsed uint64
}

type sampleWindow struct {
	start     uint64
	regionTop uint64
	raw       []byte
	size      uint64
}

func groupRegions(regions []region, metadata *vmMeta) (heapRegions, error) {
	starts, startsOK := regionTagConstant(metadata, "StartsHumongousTag")
	continues, continuesOK := regionTagConstant(metadata, "ContinuesHumongousTag")
	minimumCapacity := uint64(0)
	taggedRegions := 0
	for _, region := range regions {
		if region.capacity != 0 &&
			(minimumCapacity == 0 || region.capacity < minimumCapacity) {
			minimumCapacity = region.capacity
		}
		if region.hasTag {
			taggedRegions++
		}
	}
	if taggedRegions != 0 && taggedRegions != len(regions) {
		return heapRegions{}, unsupportedHotSpot(
			"humongous region tags are present on only part of the heap")
	}
	tagsComplete := startsOK && continuesOK && taggedRegions == len(regions)
	if !tagsComplete && !jdk8SpanningRegions(metadata) {
		return heapRegions{}, unsupportedHotSpot(
			"humongous region tags are incomplete outside the JDK 8 spanning layout")
	}
	tagsAvailable := tagsComplete
	result := heapRegions{ordinary: make([]region, 0, len(regions))}
	for index := 0; index < len(regions); index++ {
		current := regions[index]
		unit := []region{current}
		usedBytes := current.top - current.bottom
		spanningRegion := !tagsAvailable && minimumCapacity != 0 &&
			current.capacity > minimumCapacity
		if tagsAvailable && int64(current.tag) == continues {
			return heapRegions{}, unsupportedHotSpot(
				"orphan humongous continuation region")
		}
		if !spanningRegion && (!tagsAvailable || int64(current.tag) != starts) {
			result.ordinary = append(result.ordinary, current)
			result.ordinaryUsed = memsnap.SaturatingAdd(result.ordinaryUsed, usedBytes)
			continue
		}
		for next := index + 1; tagsAvailable && next < len(regions) &&
			int64(regions[next].tag) == continues; next++ {
			continuation := regions[next]
			previousEnd, valid := checkedAdd(regions[next-1].bottom,
				regions[next-1].capacity)
			if !valid {
				return heapRegions{}, unsupportedHotSpot("region boundary overflows")
			}
			if regions[next-1].capacity == 0 {
				previousEnd = regions[next-1].top
			}
			if previousEnd != continuation.bottom {
				break
			}
			unit = append(unit, continuation)
			index = next
		}
		result.humongous = append(result.humongous, regionGroup{regions: unit})
	}
	return result, nil
}

func regionTagConstant(metadata *vmMeta, name string) (int64, bool) {
	for _, typeName := range []string{"G1HeapRegionType", "HeapRegionType"} {
		if value, ok := metadata.constants[typeName+"::"+name]; ok {
			return value, true
		}
	}
	return 0, false
}

func jdk8SpanningRegions(metadata *vmMeta) bool {
	if metadata == nil || metadata.image == nil {
		return false
	}
	javaVersion := strings.TrimSpace(metadata.image.javaVersion)
	if javaVersion != "" {
		return strings.HasPrefix(javaVersion, "1.8.") || javaVersion == "8" ||
			strings.HasPrefix(javaVersion, "8.") || strings.HasPrefix(javaVersion, "8+")
	}
	version := strings.TrimSpace(metadata.image.vmRelease)
	return strings.HasPrefix(version, "1.8.") || version == "8" ||
		strings.HasPrefix(version, "8.") || strings.HasPrefix(version, "8+") ||
		legacyJDK8VMRelease(version)
}

func legacyJDK8VMRelease(version string) bool {
	update, build, ok := strings.Cut(strings.TrimPrefix(version, "25."), "-b")
	if !strings.HasPrefix(version, "25.") || !ok || update == "" || build == "" {
		return false
	}
	if _, err := strconv.ParseUint(update, 10, 32); err != nil {
		return false
	}
	_, err := strconv.ParseUint(build, 10, 32)
	return err == nil
}

func readRegions(memory processMemory, metadata *vmMeta) ([]region, error) {
	heap, err := collectedHeap(memory, metadata)
	if err != nil {
		return nil, err
	}
	return readRegionsFromHeap(memory, metadata, heap)
}

func collectedHeap(memory processMemory, metadata *vmMeta) (uint64, error) {
	heapField, ok := metadata.structs["Universe::_collectedHeap"]
	if !ok || !heapField.isStatic || heapField.address == 0 {
		return 0, errors.New("HotSpot Universe heap pointer is unavailable")
	}
	heap, err := memory.uint64(heapField.address)
	if err != nil || heap == 0 {
		return 0, errors.New("HotSpot collected heap pointer is invalid")
	}
	return heap, nil
}

func readGCSequence(memory processMemory, metadata *vmMeta) (uint32, bool, error) {
	field, ok := metadata.structs["CollectedHeap::_total_collections"]
	if !ok {
		return 0, false, nil
	}
	heap, err := collectedHeap(memory, metadata)
	if err != nil {
		return 0, true, err
	}
	address, valid := checkedAdd(heap, field.offset)
	if !valid || !metadata.image.contains(address, 4) {
		return 0, true, errors.New("HotSpot GC sequence address is invalid")
	}
	value, err := memory.uint32(address)
	return value, true, err
}

func readRegionsFromHeap(memory processMemory, metadata *vmMeta,
	heap uint64,
) ([]region, error) {
	hrmField, ok := metadata.structs["G1CollectedHeap::_hrm"]
	if !ok {
		return nil, unsupportedHotSpot("G1 heap layout is unavailable")
	}
	regionsField := firstStruct(metadata,
		"G1HeapRegionManager::_regions", "HeapRegionManager::_regions")
	if regionsField.typeString == "" {
		return nil, unsupportedHotSpot("region manager layout is unavailable")
	}
	table, valid := checkedAdd(heap, hrmField.offset)
	if !valid {
		return nil, unsupportedHotSpot("region manager address overflows")
	}
	table, valid = checkedAdd(table, regionsField.offset)
	if !valid {
		return nil, unsupportedHotSpot("region table address overflows")
	}
	baseField := metadata.structs["G1HeapRegionTable::_base"]
	lengthField := metadata.structs["G1HeapRegionTable::_length"]
	base, err := memory.uint64(table + baseField.offset)
	if err != nil || base == 0 || base&7 != 0 {
		return nil, unsupportedHotSpot("region table base is invalid")
	}
	length, err := memory.uint64(table + lengthField.offset)
	if err != nil || length == 0 || length > maxG1Regions {
		return nil, unsupportedHotSpot("region table length is invalid")
	}
	regionType := "G1HeapRegion"
	if _, ok := metadata.types[regionType]; !ok {
		regionType = "HeapRegion"
	}
	bottomField := inheritedStruct(metadata, regionType, "_bottom")
	topField := inheritedStruct(metadata, regionType, "_top")
	endField := inheritedStruct(metadata, regionType, "_end")
	if bottomField.typeString == "" || topField.typeString == "" ||
		endField.typeString == "" {
		return nil, unsupportedHotSpot("region boundary layout is unavailable")
	}
	typeField := metadata.structs[regionType+"::_type"]
	tagField := firstStruct(metadata,
		"G1HeapRegionType::_tag", "HeapRegionType::_tag")
	hasRegionTags := typeField.typeString != "" && tagField.typeString != ""
	firstOffset := min(bottomField.offset, min(topField.offset, endField.offset))
	lastFieldOffset := max(bottomField.offset, max(topField.offset, endField.offset))
	lastOffset, valid := checkedAdd(lastFieldOffset, 8)
	if !valid {
		return nil, unsupportedHotSpot("region field span overflows")
	}
	if hasRegionTags {
		tagOffset, tagValid := checkedAdd(typeField.offset, tagField.offset)
		if !tagValid {
			return nil, unsupportedHotSpot("region tag offset overflows")
		}
		firstOffset = min(firstOffset, tagOffset)
		tagEnd, tagValid := checkedAdd(tagOffset, 4)
		if !tagValid {
			return nil, unsupportedHotSpot("region tag span overflows")
		}
		lastOffset = max(lastOffset, tagEnd)
	}
	if lastOffset <= firstOffset || lastOffset-firstOffset > 4096 {
		return nil, unsupportedHotSpot("region field span is invalid")
	}
	type regionRecord struct {
		index   uint64
		address uint64
	}
	records := make([]regionRecord, 0, int(length))
	const pointersPerBatch = maxReadBytes / 8
	for begin := uint64(0); begin < length; begin += pointersPerBatch {
		count := min(uint64(pointersPerBatch), length-begin)
		pointerOffset := begin * 8
		pointerAddress, addressOK := checkedAdd(base, pointerOffset)
		if !addressOK {
			return nil, unsupportedHotSpot("region table pointer address overflows")
		}
		pointers, readErr := memory.read(pointerAddress, int(count)*8)
		if readErr != nil {
			return nil, fmt.Errorf("read HotSpot region table batch: %w", readErr)
		}
		for offset := uint64(0); offset < count; offset++ {
			regionAddress := binary.LittleEndian.Uint64(
				pointers[offset*8 : offset*8+8])
			if regionAddress == 0 {
				continue
			}
			if regionAddress&7 != 0 {
				return nil, unsupportedHotSpot("region pointer is misaligned")
			}
			records = append(records, regionRecord{
				index: begin + offset, address: regionAddress,
			})
		}
	}
	regions := make([]region, 0, length)
	var minimumRegionCapacity uint64
	for begin := 0; begin < len(records); begin += maxReadIOVs {
		endIndex := min(begin+maxReadIOVs, len(records))
		ranges := make([]memoryRange, endIndex-begin)
		for index, record := range records[begin:endIndex] {
			address, addressOK := checkedAdd(record.address, firstOffset)
			if !addressOK {
				return nil, unsupportedHotSpot("region metadata address overflows")
			}
			ranges[index] = memoryRange{
				address: address,
				size:    int(lastOffset - firstOffset),
			}
		}
		batch, readErr := memory.readv(ranges)
		if readErr != nil {
			return nil, fmt.Errorf("read HotSpot region metadata batch: %w", readErr)
		}
		for index, raw := range batch {
			record := records[begin+index]
			bottom := binary.LittleEndian.Uint64(raw[bottomField.offset-firstOffset:])
			top := binary.LittleEndian.Uint64(raw[topField.offset-firstOffset:])
			end := binary.LittleEndian.Uint64(raw[endField.offset-firstOffset:])
			capacity := end - bottom
			if bottom == 0 ||
				bottom&7 != 0 || top&7 != 0 || end&7 != 0 || top < bottom ||
				end <= bottom || top > end || capacity < 1<<20 ||
				capacity > maxJavaObjectBytes {
				return nil, unsupportedHotSpot(fmt.Sprintf(
					"region %d boundaries are invalid: bottom=%#x top=%#x end=%#x",
					record.index, bottom, top, end))
			}
			if minimumRegionCapacity == 0 || capacity < minimumRegionCapacity {
				minimumRegionCapacity = capacity
			}
			region := region{bottom: bottom, top: top, capacity: capacity}
			if hasRegionTags {
				tagOffset := typeField.offset + tagField.offset - firstOffset
				region.tag = binary.LittleEndian.Uint32(raw[tagOffset:])
				region.hasTag = true
			}
			regions = append(regions, region)
		}
	}
	if len(regions) == 0 {
		return nil, unsupportedHotSpot("region table contains no regions")
	}
	// JDK 8 exposes a humongous start Region whose _end spans its continuation
	// Regions. Its capacity is therefore an integer multiple of the configured
	// G1 Region size rather than equal to it.
	if minimumRegionCapacity&(minimumRegionCapacity-1) != 0 {
		return nil, unsupportedHotSpot("minimum region capacity is invalid")
	}
	for _, region := range regions {
		if region.capacity%minimumRegionCapacity != 0 {
			return nil, unsupportedHotSpot("region capacities are inconsistent")
		}
	}
	sort.Slice(regions, func(i, j int) bool { return regions[i].bottom < regions[j].bottom })
	normalized := regions[:0]
	for _, region := range regions {
		if len(normalized) == 0 {
			normalized = append(normalized, region)
			continue
		}
		previous := normalized[len(normalized)-1]
		previousEnd, previousOK := checkedAdd(previous.bottom, previous.capacity)
		regionEnd, regionOK := checkedAdd(region.bottom, region.capacity)
		if !previousOK || !regionOK {
			return nil, unsupportedHotSpot("region boundary overflows")
		}
		if region.bottom >= previousEnd {
			normalized = append(normalized, region)
			continue
		}
		// JDK 8 retains table entries for continuation Regions covered by the
		// humongous start Region's spanning _end. Ignore only those completely
		// contained base-size entries; all other overlap remains invalid.
		if jdk8SpanningRegions(metadata) &&
			region.capacity == minimumRegionCapacity && regionEnd <= previousEnd {
			continue
		}
		if region.bottom < previousEnd {
			return nil, unsupportedHotSpot("regions overlap")
		}
	}
	return normalized, nil
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
		totalUsed = memsnap.SaturatingAdd(totalUsed, used)
		totalSlots = memsnap.SaturatingAdd(totalSlots, slots)
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
				"used-byte-weighted HotSpot sampling stopped: %v", err)
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
				err)
		}
		read, readErr := unix.ProcessVMReadv(memory.pid, local, remote, 0)
		if err := memory.check(); err != nil {
			return fmt.Sprintf(
				"used-byte-weighted HotSpot sampling stopped after process_vm_readv: %v",
				err)
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
