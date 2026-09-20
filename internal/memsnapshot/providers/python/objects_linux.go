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
	"errors"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

const (
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
	pyTPFlagsDictSubclass   = uint64(1 << 29)
	maxInstanceFields       = 64
	maxCStringBytes         = 512
	maxTypeMetadata         = 32_768
	maxTypeNameBytes        = 8 << 20
	maxInvalidTypes         = 65_536
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

func plausiblePtr(address uint64) bool {
	return plausibleAddr(address) && address&7 == 0
}

func plausibleAddr(address uint64) bool {
	return address >= 0x10000 && address < 1<<56
}
