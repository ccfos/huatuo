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
	"bytes"
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ccfos/huatuo/internal/memsnap"
)

const (
	pyGCHeadSize            = uint64(16)
	pyGenerationSize        = uint64(24)
	pyTypeReadSize          = 272
	pyTypeNameOffset        = 24
	pyTypeBasicOffset       = 32
	pyTypeItemOffset        = 40
	pyTypeFlagsOffset       = 168
	pyTypeBaseOffset        = 256
	pyTypeDictOffset        = 264
	pyObjectTypeOffset      = 8
	pyObjectSizeOffset      = 16
	pyTPFlagsManagedWeakref = uint64(1 << 3)
	pyTPFlagsManagedDict    = uint64(1 << 4)
	pyTPFlagsPreheader      = pyTPFlagsManagedWeakref | pyTPFlagsManagedDict
	pyTPFlagsHeapType       = uint64(1 << 9)
	maxInstanceFields       = 64
	maxCStringBytes         = 512
	maxTypeMetadata         = 32_768
	maxTypeNameBytes        = 8 << 20
	maxInvalidTypes         = 65_536
	maxScannedObjects       = 10_000_000
	maxEstimatedObjectBytes = uint64(1 << 40)
)

type typeInfo struct {
	address   uint64
	name      string
	basicsize int64
	itemsize  int64
	flags     uint64
	base      uint64
	dict      uint64
}

type dictEntry struct {
	name  string
	value uint64
}

func alignUp(value, alignment uint64) uint64 {
	return (value + alignment - 1) &^ (alignment - 1)
}

func (c *scanner) deadlineReached() bool {
	return !c.deadline.IsZero() && !time.Now().Before(c.deadline)
}

func (c *scanner) findGenerationOffset(baseAddress uint64, raw []byte) int {
	bestOffset := -1
	bestScore := -1
	for offset := 0; offset+int(3*pyGenerationSize) <= len(raw); offset += 8 {
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
	if !plausiblePtr(next) || !plausiblePtr(previous) {
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
	if !plausiblePtr(initialNext) || !plausiblePtr(initialPrevious) ||
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
		aggregate = &memsnap.ObjectAggregate{TypeName: typeInfo.name}
		c.aggregates[typeAddress] = aggregate
	}
	aggregate.Count++
	aggregate.ShallowBytes = memsnap.SaturatingAdd(aggregate.ShallowBytes, objectSize)
	return true
}

func (c *scanner) typeDictStrings(address uint64) (string, string) {
	if !plausiblePtr(address) {
		return "", ""
	}
	raw, err := c.memory.read(address, 48)
	if err != nil {
		return "", ""
	}
	keys := c.image.order.Uint64(raw[32:40])
	values := c.image.order.Uint64(raw[40:48])
	entries, err := c.dictEntries(keys)
	if err != nil || len(entries) == 0 || len(entries) > maxInstanceFields {
		return "", ""
	}
	var valueRaw []byte
	if values != 0 {
		valueRaw, err = c.memory.read(values, len(entries)*8)
		if err != nil {
			return "", ""
		}
	}
	var moduleAddress, qualnameAddress uint64
	for index, entry := range entries {
		value := entry.value
		if values != 0 {
			value = c.image.order.Uint64(valueRaw[index*8 : index*8+8])
		}
		if !plausiblePtr(value) {
			continue
		}
		switch entry.name {
		case "__module__":
			moduleAddress = value
		case "__qualname__":
			qualnameAddress = value
		}
	}
	module, _ := c.readASCIIUnicode(moduleAddress, 256)
	qualname, _ := c.readASCIIUnicode(qualnameAddress, 256)
	return module, qualname
}

func (c *scanner) dictEntries(address uint64) ([]dictEntry,
	error,
) {
	if !plausiblePtr(address) {
		return nil, errors.New("invalid dictionary keys pointer")
	}
	header, err := c.memory.read(address, 40)
	if err != nil {
		return nil, err
	}
	// CPython 3.11+ compact keys header.
	logSize := header[8]
	logIndexBytes := header[9]
	kind := header[10]
	nentries := int64(c.image.order.Uint64(header[24:32]))
	if logSize <= 30 && logIndexBytes <= 30 && kind <= 2 && nentries > 0 &&
		nentries <= maxInstanceFields {
		indicesBytes := uint64(1) << logIndexBytes
		entryAddress := address + 32 + indicesBytes
		if entries := c.keyEntries(entryAddress, int(nentries), kind == 0); len(entries) != 0 {
			return entries, nil
		}
	}
	// CPython 3.8-3.10 keys header.
	dictSize := int64(c.image.order.Uint64(header[8:16]))
	oldEntries := int64(c.image.order.Uint64(header[32:40]))
	if dictSize <= 0 || dictSize > 1<<30 || oldEntries <= 0 ||
		oldEntries > maxInstanceFields || dictSize&(dictSize-1) != 0 {
		return nil, errors.New("unrecognized dictionary keys layout")
	}
	indexSize := uint64(1)
	if dictSize > 0xff {
		indexSize = 2
	}
	if dictSize > 0xffff {
		indexSize = 4
	}
	if uint64(dictSize) > math.MaxUint32 {
		indexSize = 8
	}
	entryAddress := alignUp(address+40+uint64(dictSize)*indexSize, 8)
	unicodeEntries := c.keyEntries(entryAddress, int(oldEntries), false)
	generalEntries := c.keyEntries(entryAddress, int(oldEntries), true)
	if validDictEntries(generalEntries) > validDictEntries(unicodeEntries) {
		return generalEntries, nil
	}
	if validDictEntries(unicodeEntries) != 0 {
		return unicodeEntries, nil
	}
	return nil, errors.New("dictionary key entries are unreadable")
}

func (c *scanner) keyEntries(address uint64, count int,
	general bool,
) []dictEntry {
	stride := 16
	keyOffset := 0
	valueOffset := 8
	if general {
		stride = 24
		keyOffset = 8
		valueOffset = 16
	}
	raw, err := c.memory.read(address, count*stride)
	if err != nil {
		return nil
	}
	entries := make([]dictEntry, count)
	valid := 0
	for index := 0; index < count; index++ {
		key := c.image.order.Uint64(raw[index*stride+keyOffset : index*stride+keyOffset+8])
		if key == 0 {
			continue
		}
		name, nameErr := c.readASCIIUnicode(key, 256)
		if nameErr == nil && name != "" {
			entries[index] = dictEntry{
				name:  name,
				value: c.image.order.Uint64(raw[index*stride+valueOffset : index*stride+valueOffset+8]),
			}
			valid++
		}
	}
	if valid == 0 {
		return nil
	}
	return entries
}

func validDictEntries(entries []dictEntry) int {
	valid := 0
	for _, entry := range entries {
		if entry.name != "" {
			valid++
		}
	}
	return valid
}

func (c *scanner) typeInfo(address uint64) (typeInfo, error) {
	if cached, ok := c.types[address]; ok {
		return cached, nil
	}
	if _, invalid := c.invalidTypes[address]; invalid {
		return typeInfo{}, errors.New("invalid cached Python type pointer")
	}
	if !plausiblePtr(address) {
		return typeInfo{}, errors.New("invalid Python type pointer")
	}
	if len(c.types) >= maxTypeMetadata {
		c.partial = "Python type metadata limit reached"
		return typeInfo{}, errors.New(c.partial)
	}
	nameOffset := c.image.layout.typeNameOffset
	flagsOffset := c.image.layout.typeFlagsOffset
	if nameOffset < pyTypeNameOffset {
		c.partial = "CPython type metadata layout is invalid"
		return typeInfo{}, errors.New(c.partial)
	}
	typeDelta := nameOffset - pyTypeNameOffset
	typeReadSize := pyTypeReadSize + int(typeDelta)
	raw, err := c.memory.read(address, typeReadSize)
	if err != nil {
		c.cacheInvalidType(address)
		return typeInfo{}, err
	}
	basicOffset := uint64(pyTypeBasicOffset) + typeDelta
	itemOffset := uint64(pyTypeItemOffset) + typeDelta
	baseOffset := uint64(pyTypeBaseOffset) + typeDelta
	dictOffset := uint64(pyTypeDictOffset) + typeDelta
	if nameOffset+8 > uint64(len(raw)) || flagsOffset+8 > uint64(len(raw)) ||
		dictOffset+8 > uint64(len(raw)) {
		c.partial = "CPython type metadata layout is invalid"
		return typeInfo{}, errors.New(c.partial)
	}
	nameAddress := c.image.order.Uint64(raw[nameOffset : nameOffset+8])
	name, err := c.readCString(nameAddress, maxCStringBytes)
	if err != nil || name == "" {
		c.cacheInvalidType(address)
		return typeInfo{}, errors.New("invalid Python type name")
	}
	result := typeInfo{
		address:   address,
		name:      name,
		basicsize: int64(c.image.order.Uint64(raw[basicOffset : basicOffset+8])),
		itemsize:  int64(c.image.order.Uint64(raw[itemOffset : itemOffset+8])),
		flags:     c.image.order.Uint64(raw[flagsOffset : flagsOffset+8]),
		base:      c.image.order.Uint64(raw[baseOffset : baseOffset+8]),
		dict:      c.image.order.Uint64(raw[dictOffset : dictOffset+8]),
	}
	if result.basicsize < 16 || result.basicsize > 1<<30 ||
		result.itemsize < 0 || result.itemsize > 1<<24 {
		c.cacheInvalidType(address)
		return typeInfo{}, errors.New("invalid Python type size")
	}
	if result.flags&pyTPFlagsHeapType != 0 {
		result.name = c.heapTypeName(result)
	} else if !strings.Contains(result.name, ".") {
		result.name = "builtins." + result.name
	}
	if len(result.name) > 512 {
		result.name = result.name[:512]
	}
	if len(result.name) > maxTypeNameBytes-c.typeNameBytes {
		c.partial = "Python type name memory limit reached"
		return typeInfo{}, errors.New(c.partial)
	}
	c.typeNameBytes += len(result.name)
	c.types[address] = result
	return result, nil
}

func (c *scanner) cacheInvalidType(address uint64) {
	if len(c.invalidTypes) >= maxInvalidTypes {
		c.partial = "invalid Python type cache limit reached"
		return
	}
	c.invalidTypes[address] = struct{}{}
}

func (c *scanner) heapTypeName(typeInfo typeInfo) string {
	module, qualname := c.typeDictStrings(typeInfo.dict)
	if module == "" {
		return typeInfo.name
	}
	if qualname == "" {
		qualname = strings.TrimPrefix(typeInfo.name, module+".")
	}
	return module + "." + qualname
}

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
		objectHead[sizeOffset : sizeOffset+8]))
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
	size = memsnap.SaturatingAdd(size, pyGCHeadSize)
	if typeInfo.flags&pyTPFlagsPreheader != 0 {
		size = memsnap.SaturatingAdd(size, 16)
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

func (c *scanner) objectItems(objectHead []byte) (int64, bool) {
	sizeOffset := c.image.layout.objectSizeOffset
	if sizeOffset+8 > uint64(len(objectHead)) {
		return 0, false
	}
	items := int64(c.image.order.Uint64(
		objectHead[sizeOffset : sizeOffset+8]))
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

func (c *scanner) isListType(typeInfo typeInfo) bool {
	if cached, ok := c.listTypes[typeInfo.address]; ok {
		return cached
	}
	root := typeInfo.address
	result := false
	for depth := 0; depth < 16; depth++ {
		if typeInfo.flags&pyTPFlagsHeapType == 0 {
			name := typeInfo.name
			if !strings.Contains(name, ".") {
				name = "builtins." + name
			}
			if name == "builtins.list" {
				result = true
				break
			}
		}
		if typeInfo.base == 0 {
			break
		}
		base, err := c.listTypeInfo(typeInfo.base)
		if err != nil || base.address == typeInfo.address {
			break
		}
		typeInfo = base
	}
	c.listTypes[root] = result
	return result
}

// listTypeInfo reads only the metadata needed to recognize list subclasses.
// It is deliberately isolated from typeInfo so failures cannot mark the GC
// census partial or consume its retained metadata budget.
func (c *scanner) listTypeInfo(address uint64) (typeInfo, error) {
	if cached, ok := c.types[address]; ok {
		return cached, nil
	}
	if !plausiblePtr(address) {
		return typeInfo{}, errors.New("invalid Python base type pointer")
	}
	nameOffset := c.image.layout.typeNameOffset
	flagsOffset := c.image.layout.typeFlagsOffset
	if nameOffset < pyTypeNameOffset {
		return typeInfo{}, errors.New("invalid Python base type layout")
	}
	typeDelta := nameOffset - pyTypeNameOffset
	raw, err := c.memory.read(address, pyTypeReadSize+int(typeDelta))
	if err != nil {
		return typeInfo{}, err
	}
	baseOffset := uint64(pyTypeBaseOffset) + typeDelta
	if nameOffset > uint64(len(raw))-8 || flagsOffset > uint64(len(raw))-8 ||
		baseOffset > uint64(len(raw))-8 {
		return typeInfo{}, errors.New("invalid Python base type layout")
	}
	nameAddress := c.image.order.Uint64(raw[nameOffset : nameOffset+8])
	name, err := c.readCString(nameAddress, maxCStringBytes)
	if err != nil || name == "" {
		return typeInfo{}, errors.New("invalid Python base type name")
	}
	return typeInfo{
		address: address, name: name,
		flags: c.image.order.Uint64(raw[flagsOffset : flagsOffset+8]),
		base:  c.image.order.Uint64(raw[baseOffset : baseOffset+8]),
	}, nil
}

func (c *scanner) readCString(address uint64, limit int) (string, error) {
	if !plausibleAddr(address) {
		return "", errors.New("invalid C string pointer")
	}
	valueRaw := make([]byte, 0, 64)
	for offset := 0; offset < limit; {
		chunkSize := 32
		if remaining := limit - offset; remaining < chunkSize {
			chunkSize = remaining
		}
		chunk, err := c.memory.read(address+uint64(offset), chunkSize)
		if err != nil {
			// A short type name can sit at the end of an ELF mapping. Avoid
			// rejecting it merely because a speculative chunk crossed the next
			// unmapped page.
			chunk, err = c.memory.read(address+uint64(offset), 1)
			if err != nil {
				return "", err
			}
			chunkSize = 1
		}
		if end := bytes.IndexByte(chunk, 0); end >= 0 {
			valueRaw = append(valueRaw, chunk[:end]...)
			value := string(valueRaw)
			if !utf8.ValidString(value) {
				return "", errors.New("invalid UTF-8 C string")
			}
			for _, character := range value {
				if character < 0x20 || character == 0x7f {
					return "", errors.New("non-printable C string")
				}
			}
			return value, nil
		}
		valueRaw = append(valueRaw, chunk...)
		offset += chunkSize
	}
	return "", errors.New("unterminated C string")
}

func (c *scanner) readASCIIUnicode(address uint64, limit int) (string, error) {
	header, err := c.memory.read(address, 40)
	if err != nil {
		return "", err
	}
	length := int64(c.image.order.Uint64(header[16:24]))
	state := c.image.order.Uint32(header[32:36])
	compact := state&(1<<5) != 0
	ascii := state&(1<<6) != 0
	if !compact || !ascii || length <= 0 || length > int64(limit) {
		return "", errors.New("dictionary key is not compact ASCII")
	}
	raw, err := c.memory.read(address+c.image.layout.unicodeDataOffset, int(length))
	if err != nil {
		return "", err
	}
	for _, character := range raw {
		if character < 0x20 || character > 0x7e {
			return "", errors.New("dictionary key is not printable ASCII")
		}
	}
	return string(raw), nil
}

func (c *scanner) entries() []memsnap.Entry {
	result := make([]memsnap.Entry, 0, len(c.aggregates))
	for _, aggregate := range c.aggregates {
		if aggregate.Count != 0 {
			aggregate.AverageBytes = float64(aggregate.ShallowBytes) /
				float64(aggregate.Count)
		}
		result = append(result, memsnap.Entry{
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

func plausiblePtr(address uint64) bool {
	return plausibleAddr(address) && address&7 == 0
}

func plausibleAddr(address uint64) bool {
	return address >= 0x10000 && address < 1<<56
}
