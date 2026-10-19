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
	"github.com/ccfos/huatuo/internal/memsnapshot"
)

const maxEstimatedObjectBytes = uint64(1 << 40)

func (c *scanner) baseSize(objectHead []byte, typeInfo typeInfo) uint64 {
	size := uint64(typeInfo.basicsize)
	if typeInfo.itemsize == 0 {
		return alignUp(size, 8)
	}
	sizeOffset := c.image.layout.objectSizeOffset
	if sizeOffset+8 > uint64(len(objectHead)) {
		return alignUp(size, 8)
	}
	items := int64(c.image.order.Uint64(
		objectHead[sizeOffset : sizeOffset+8],
	))
	if items < 0 {
		items = -items
	}
	if uint64(items) > (maxEstimatedObjectBytes-size)/uint64(typeInfo.itemsize) {
		return alignUp(size, 8)
	}
	estimate := alignUp(size+uint64(items)*uint64(typeInfo.itemsize), 8)
	if estimate > maxEstimatedObjectBytes {
		return alignUp(size, 8)
	}
	return estimate
}

func (c *scanner) objectSize(address uint64, objectHead []byte,
	typeInfo typeInfo,
) uint64 {
	size := c.baseSize(objectHead, typeInfo)
	// Every object reached here has a GC head. Managed dict/weakref pointers are
	// additional preheader words in CPython 3.11+.
	size = memsnapshot.SaturatingAdd(size, pyGCHeadSize)
	if typeInfo.flags&pyTPFlagsPreheader != 0 {
		size = memsnapshot.SaturatingAdd(size, 16)
	}
	if typeInfo.flags&pyTPFlagsDictSubclass != 0 {
		return boundedObjectAdd(size, c.dictExtraSize(address))
	}
	// A list's directly owned item-pointer buffer refines the byte estimate
	// only. Failure to read mutable capacity metadata does not make the GC-list
	// census incomplete.
	logicalItems, logicalOK := c.objectItems(objectHead)
	if !logicalOK || !c.isListType(typeInfo) {
		return size
	}
	if err := c.memory.readInto(address+24, c.objectScratch[:]); err == nil {
		buffer := c.image.order.Uint64(c.objectScratch[:8])
		allocated := int64(c.image.order.Uint64(c.objectScratch[8:]))
		if listBufferValid(logicalItems, allocated, buffer) {
			size = boundedObjectAdd(size, uint64(allocated)*8)
		}
	}
	return size
}

// dictExtraSize follows CPython's dict.__sizeof__ ownership rules. Only
// capacity metadata is read: referenced key/value objects are counted separately.
// Unreadable or inconsistent metadata leaves the base estimate unchanged.
func (c *scanner) dictExtraSize(address uint64) uint64 {
	minor := c.image.version.minor
	if c.image.version.major != 3 || minor < 8 || minor > 14 {
		return 0
	}
	var pointers [16]byte
	offset := c.image.layout.objectTypeOffset + 24
	if address > ^uint64(0)-offset || c.memory.readInto(address+offset, pointers[:]) != nil {
		return 0
	}
	order := c.image.order
	keys, values := order.Uint64(pointers[:8]), order.Uint64(pointers[8:])
	if !plausiblePtr(keys) || (values != 0 && !plausiblePtr(values)) {
		return 0
	}
	var header [40]byte
	headerSize := uint64(40)
	if minor >= 11 {
		headerSize = 32
	}
	if c.memory.readInto(keys, header[:headerSize]) != nil {
		return 0
	}
	refcnt := order.Uint64(header[:8])
	tableSize := order.Uint64(header[8:16])
	entrySize := uint64(24)
	if minor >= 11 {
		if header[8] > 30 || header[10] > 2 {
			return 0
		}
		tableSize = uint64(1) << header[8]
		if header[10] != 0 {
			entrySize = 16
		}
	}
	if refcnt == 0 || tableSize == 0 || tableSize > 1<<30 || tableSize&(tableSize-1) != 0 {
		return 0
	}
	indexWidth := uint64(1)
	if tableSize > 0xff {
		indexWidth = 2
	}
	if tableSize > 0xffff {
		indexWidth = 4
	}
	indices := tableSize * indexWidth
	if minor >= 11 && (header[9] > 32 || uint64(1)<<header[9] != indices) {
		return 0
	}
	capacity := tableSize * 2 / 3
	usable := order.Uint64(header[headerSize-16 : headerSize-8])
	entries := order.Uint64(header[headerSize-8 : headerSize])
	if usable > capacity || entries > capacity-usable {
		return 0
	}
	var extra uint64
	if values != 0 {
		if minor >= 11 {
			if header[10] != 2 {
				return 0
			}
			capacity = usable + entries
		}
		extra = capacity * 8
	}
	// Shared keys (including the singleton empty table) are not charged to
	// each dictionary, matching CPython rather than duplicating shared storage.
	if refcnt == 1 {
		extra += headerSize + indices + (tableSize*2/3)*entrySize
	}
	return extra
}

func (c *scanner) objectItems(objectHead []byte) (int64, bool) {
	sizeOffset := c.image.layout.objectSizeOffset
	if sizeOffset+8 > uint64(len(objectHead)) {
		return 0, false
	}
	items := int64(c.image.order.Uint64(
		objectHead[sizeOffset : sizeOffset+8],
	))
	return items, items >= 0
}

func listBufferValid(logicalItems, allocated int64, buffer uint64) bool {
	if logicalItems < 0 || allocated < logicalItems || allocated <= 0 ||
		uint64(allocated) > maxEstimatedObjectBytes/8 || !plausibleAddr(buffer) {
		return false
	}
	return true
}

func boundedObjectAdd(size, extra uint64) uint64 {
	if extra > maxEstimatedObjectBytes || size > maxEstimatedObjectBytes-extra {
		return size
	}
	return size + extra
}
