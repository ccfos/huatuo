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

package python

import (
	"context"
	"sort"
	"time"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

const (
	pyGCHeadSize      = uint64(16)
	pyGenerationSize  = uint64(24)
	maxScannedObjects = 10_000_000
)

func (c *scanner) deadlineReached() bool {
	return !c.deadline.IsZero() && !time.Now().Before(c.deadline)
}

func (c *scanner) findGenerationOffset(baseAddress uint64, raw []byte) int {
	bestOffset := -1
	bestScore := -1
	for offset := 0; offset+int(3*pyGenerationSize) <= len(raw); offset += 4 {
		score := 0
		valid := true
		for generation := 0; generation < 3; generation++ {
			base := offset + generation*int(pyGenerationSize)
			head := baseAddress + uint64(base)
			next := c.image.order.Uint64(raw[base:base+8]) &^ 3
			previous := c.image.order.Uint64(raw[base+8:base+16]) &^ 3
			threshold := int32(c.image.order.Uint32(raw[base+16 : base+20]))
			count := int32(c.image.order.Uint32(raw[base+20 : base+24]))
			// gc.set_threshold() intentionally accepts every C int value. Treat
			// thresholds as scoring hints, never as structural validation.
			if count < 0 || !c.validGenerationLinks(head, next, previous) {
				valid = false
				break
			}
			if next == head && previous == head {
				score++
			}
			if generation == 0 && threshold != 0 {
				score += 2
			}
		}
		if valid && score > bestScore {
			bestOffset = offset
			bestScore = score
		}
	}
	return bestOffset
}

func (c *scanner) validGenerationLinks(head, next, previous uint64) bool {
	if (next != head && !plausiblePtr(next)) ||
		(previous != head && !plausiblePtr(previous)) ||
		(next == head) != (previous == head) {
		return false
	}
	if next != head {
		raw, err := c.memory.read(next, 16)
		if err != nil || c.image.order.Uint64(raw[8:16])&^3 != head {
			return false
		}
	}
	if previous != head {
		raw, err := c.memory.read(previous, 8)
		if err != nil || c.image.order.Uint64(raw[:8])&^3 != head {
			return false
		}
	}
	return true
}

func (c *scanner) walkGeneration(ctx context.Context, head uint64) bool {
	var header [56]byte
	objectHeadBytes := c.image.layout.objectTypeOffset + 8
	if sizeBytes := c.image.layout.objectSizeOffset + 8; sizeBytes > objectHeadBytes {
		objectHeadBytes = sizeBytes
	}
	if objectHeadBytes < 24 {
		objectHeadBytes = 24
	}
	if objectHeadBytes > uint64(len(header))-pyGCHeadSize {
		c.partial = "CPython object header layout is invalid"
		return false
	}
	if err := c.memory.readInto(head, header[:16]); err != nil {
		c.partial = "GC generation head became unreadable"
		return false
	}
	initialNext := c.image.order.Uint64(header[:8]) &^ 3
	initialPrevious := c.image.order.Uint64(header[8:16]) &^ 3
	if (initialNext != head && !plausiblePtr(initialNext)) ||
		(initialPrevious != head && !plausiblePtr(initialPrevious)) ||
		(initialNext == head) != (initialPrevious == head) {
		c.partial = "GC generation endpoints are inconsistent"
		return false
	}
	next := initialNext
	previous := head
	for next != head {
		if c.scannedObjects >= maxScannedObjects {
			c.partial = "CPython GC-tracked object safety limit reached"
			return false
		}
		if c.scannedObjects&31 == 0 && (ctx.Err() != nil || c.deadlineReached()) {
			c.partial = "deadline reached during external object census"
			return false
		}
		if !plausiblePtr(next) {
			c.partial = "GC generation contains an invalid pointer"
			return false
		}
		headerBytes := int(pyGCHeadSize + objectHeadBytes)
		if readErr := c.memory.readInto(next, header[:headerBytes]); readErr != nil {
			c.partial = "GC object header became unreadable"
			return false
		}
		following := c.image.order.Uint64(header[:8]) &^ 3
		linkedPrevious := c.image.order.Uint64(header[8:16]) &^ 3
		if linkedPrevious != previous {
			c.partial = "GC generation changed during external census"
			return false
		}
		objectAddress := next + pyGCHeadSize
		c.scannedObjects++
		if !c.addObject(objectAddress, header[16:headerBytes]) {
			return false
		}
		previous = next
		next = following
	}
	if err := c.memory.readInto(head, header[:16]); err != nil {
		c.partial = "GC generation head became unreadable during endpoint check"
		return false
	}
	finalNext := c.image.order.Uint64(header[:8]) &^ 3
	finalPrevious := c.image.order.Uint64(header[8:16]) &^ 3
	if finalNext != initialNext || finalPrevious != initialPrevious ||
		finalPrevious != previous {
		c.partial = "GC generation changed during external census"
		return false
	}
	return true
}

func (c *scanner) addObject(address uint64, objectHead []byte) bool {
	typeOffset := c.image.layout.objectTypeOffset
	if typeOffset+8 > uint64(len(objectHead)) {
		c.partial = "CPython object header layout is invalid"
		return false
	}
	typeAddress := c.image.order.Uint64(objectHead[typeOffset : typeOffset+8])
	typeInfo, err := c.typeInfo(typeAddress)
	if err != nil {
		if c.partial != "" {
			return false
		}
		c.skippedObjects++
		return true
	}
	objectSize := c.objectSize(address, objectHead, typeInfo)
	aggregate := c.aggregates[typeAddress]
	if aggregate == nil {
		aggregate = &memsnapshot.ObjectAggregate{TypeName: typeInfo.name}
		c.aggregates[typeAddress] = aggregate
	}
	aggregate.Count++
	aggregate.ShallowBytes = memsnapshot.SaturatingAdd(aggregate.ShallowBytes, objectSize)
	return true
}

func (c *scanner) entries() []memsnapshot.Entry {
	result := make([]memsnapshot.Entry, 0, len(c.aggregates))
	for _, aggregate := range c.aggregates {
		if aggregate.Count != 0 {
			aggregate.AverageBytes = float64(aggregate.ShallowBytes) /
				float64(aggregate.Count)
		}
		result = append(result, memsnapshot.Entry{
			Kind: "gc_tracked_object_type", Name: aggregate.TypeName,
			Bytes: aggregate.ShallowBytes, Objects: aggregate.Count,
			AverageBytes: aggregate.AverageBytes,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Bytes != result[j].Bytes {
			return result[i].Bytes > result[j].Bytes
		}
		if result[i].Name != result[j].Name {
			return result[i].Name < result[j].Name
		}
		return result[i].Objects > result[j].Objects
	})
	return result
}
