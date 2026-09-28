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
)

const (
	maxSampleBytes          = 32 << 20
	windowBytes             = 4 << 10
	maxKlassAttempts        = 1024
	maxBatchKlassCandidates = 4096
)

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

// readBatchEnd bounds each fixed-size sample read to the process_vm_readv IOV
// limit. The production 4 KiB windows stay below maxReadBytes at this limit.
func readBatchEnd(begin, total int) int {
	end := begin + maxReadIOVs
	if end > total {
		end = total
	}
	return end
}

// scanWindows reads one bounded process_vm_readv batch and
// hands it to the classifier before issuing the next batch. At most one batch
// of victim heap bytes is retained at a time. The visitor borrows batch and raw
// slices only until it returns; the next batch reuses their backing memory.
func scanWindows(memory processMemory, regions []region,
	seed, budget, windowBytes uint64,
	visit func([]sampleWindow) bool,
) string {
	windows := planWindows(regions, seed, budget, windowBytes)
	var buffer []byte
	local := make([]unix.Iovec, min(maxReadIOVs, len(windows)))
	remote := make([]unix.RemoteIovec, len(local))
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
		if cap(buffer) < batchBytes {
			buffer = make([]byte, batchBytes)
		}
		buffer = buffer[:batchBytes]
		offset := 0
		for index := range batch {
			length := int(batch[index].size)
			batch[index].raw = buffer[offset : offset+length]
			local[index] = unix.Iovec{Base: &batch[index].raw[0], Len: uint64(length)}
			remote[index] = unix.RemoteIovec{Base: uintptr(batch[index].start), Len: length}
			offset += length
		}
		if err := memory.check(); err != nil {
			return fmt.Sprintf(
				"used-byte-weighted HotSpot sampling stopped before process_vm_readv: %v",
				err,
			)
		}
		read, readErr := unix.ProcessVMReadv(memory.pid, local[:len(batch)], remote[:len(batch)], 0)
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
