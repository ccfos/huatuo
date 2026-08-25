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
	"math"
	"sort"
	"strings"

	"github.com/ccfos/huatuo/internal/memsnap"
)

const (
	maxJavaObjectBytes = 1 << 40
	maxCachedKlasses   = 65_536
)

type klass struct {
	name         string
	layoutHelper int32
}

type ptrEncoding struct {
	compressedKlass bool
	klassBase       uint64
	klassShift      uint
}

type classSample struct {
	count uint64
	bytes uint64
}

type klassHeader struct {
	address     uint64
	namePointer uint64
	layout      int32
}

func firstStruct(metadata *vmMeta, names ...string) vmStruct {
	for _, name := range names {
		if field, ok := metadata.structs[name]; ok {
			return field
		}
	}
	return vmStruct{}
}

func inheritedStruct(metadata *vmMeta, typeName, fieldName string) vmStruct {
	for depth := 0; typeName != "" && depth < 16; depth++ {
		if field, ok := metadata.structs[typeName+"::"+fieldName]; ok {
			return field
		}
		// JDK 11's VMType table omits the G1ContiguousSpace to Space link,
		// although the C++ layout still embeds Space at offset zero.
		if typeName == "G1ContiguousSpace" && fieldName == "_bottom" {
			return metadata.structs["Space::_bottom"]
		}
		typeName = metadata.types[typeName].superclass
	}
	return vmStruct{}
}

func readKlass(memory processMemory, metadata *vmMeta,
	address uint64,
) (*klass, error) {
	if address == 0 || address&7 != 0 {
		return nil, errors.New("HotSpot Klass address is invalid")
	}
	layoutField := metadata.structs["Klass::_layout_helper"]
	nameField := metadata.structs["Klass::_name"]
	layoutAddress, ok := checkedAdd(address, layoutField.offset)
	if !ok || !metadata.image.contains(layoutAddress, 4) {
		return nil, errors.New("HotSpot Klass layout address is unreadable")
	}
	nameAddress, ok := checkedAdd(address, nameField.offset)
	if !ok || !metadata.image.contains(nameAddress, 8) {
		return nil, errors.New("HotSpot Klass name address is unreadable")
	}
	layout, err := memory.uint32(layoutAddress)
	if err != nil {
		return nil, err
	}
	namePointer, err := memory.uint64(nameAddress)
	if err != nil || namePointer == 0 {
		return nil, errors.New("HotSpot Klass name is unavailable")
	}
	name, err := readSymbol(memory, metadata, namePointer)
	if err != nil {
		return nil, fmt.Errorf("read HotSpot Klass name: %w", err)
	}
	return &klass{
		name: name, layoutHelper: int32(layout),
	}, nil
}

// readKlassBatch resolves fixed Klass fields and Symbol names in bounded
// process_vm_readv batches. Failed batches are skipped to keep the number of
// victim-memory syscalls bounded under memory pressure.
func readKlassBatch(memory processMemory, metadata *vmMeta,
	addresses []uint64,
) map[uint64]*klass {
	resolved := make(map[uint64]*klass, len(addresses))
	if len(addresses) == 0 || metadata == nil {
		return resolved
	}
	layoutField := metadata.structs["Klass::_layout_helper"]
	nameField := metadata.structs["Klass::_name"]
	firstOffset := min(layoutField.offset, nameField.offset)
	layoutEnd, layoutOK := checkedAdd(layoutField.offset, 4)
	nameEnd, nameOK := checkedAdd(nameField.offset, 8)
	if !layoutOK || !nameOK {
		return resolved
	}
	lastOffset := max(layoutEnd, nameEnd)
	if lastOffset <= firstOffset || lastOffset-firstOffset > 4096 {
		return resolved
	}

	headers := make([]klassHeader, 0, len(addresses))
	for begin := 0; begin < len(addresses); begin += maxReadIOVs {
		end := min(begin+maxReadIOVs, len(addresses))
		ranges := make([]memoryRange, 0, end-begin)
		validAddresses := make([]uint64, 0, end-begin)
		for _, address := range addresses[begin:end] {
			start, ok := checkedAdd(address, firstOffset)
			if !ok || address == 0 || address&7 != 0 ||
				!metadata.image.contains(start, lastOffset-firstOffset) {
				continue
			}
			ranges = append(ranges, memoryRange{
				address: start, size: int(lastOffset - firstOffset),
			})
			validAddresses = append(validAddresses, address)
		}
		batch, err := memory.readv(ranges)
		if err != nil {
			continue
		}
		for index, raw := range batch {
			namePointer := binary.LittleEndian.Uint64(raw[nameField.offset-firstOffset:])
			if namePointer == 0 {
				continue
			}
			headers = append(headers, klassHeader{
				address: validAddresses[index], namePointer: namePointer,
				layout: int32(binary.LittleEndian.Uint32(
					raw[layoutField.offset-firstOffset:])),
			})
		}
	}
	resolveKlassNames(memory, metadata, headers, resolved)
	return resolved
}

func resolveKlassNames(memory processMemory, metadata *vmMeta,
	headers []klassHeader, resolved map[uint64]*klass,
) {
	lengthField := metadata.structs["Symbol::_length"]
	bodyField, ok := metadata.structs["Symbol::_body[0]"]
	if !ok {
		bodyField, ok = metadata.structs["Symbol::_body"]
	}
	if !ok {
		return
	}
	for begin := 0; begin < len(headers); begin += maxReadIOVs {
		end := min(begin+maxReadIOVs, len(headers))
		ranges := make([]memoryRange, 0, end-begin)
		validHeaders := make([]klassHeader, 0, end-begin)
		for _, header := range headers[begin:end] {
			address, valid := checkedAdd(header.namePointer, lengthField.offset)
			if !valid || !metadata.image.contains(address, 2) {
				continue
			}
			ranges = append(ranges, memoryRange{address: address, size: 2})
			validHeaders = append(validHeaders, header)
		}
		lengths, err := memory.readv(ranges)
		if err != nil {
			continue
		}
		nameRanges := make([]memoryRange, 0, len(lengths))
		nameHeaders := make([]klassHeader, 0, len(lengths))
		for index, raw := range lengths {
			length := binary.LittleEndian.Uint16(raw)
			address, valid := checkedAdd(validHeaders[index].namePointer,
				bodyField.offset)
			if length == 0 || length > maxHotSpotStringBytes || !valid ||
				!metadata.image.contains(address, uint64(length)) {
				continue
			}
			nameRanges = append(nameRanges, memoryRange{address: address, size: int(length)})
			nameHeaders = append(nameHeaders, validHeaders[index])
		}
		names, err := memory.readv(nameRanges)
		if err != nil {
			continue
		}
		for index, name := range names {
			header := nameHeaders[index]
			resolved[header.address] = &klass{
				name: string(name), layoutHelper: header.layout,
			}
		}
	}
}

func readSymbol(memory processMemory, metadata *vmMeta,
	address uint64,
) (string, error) {
	lengthField, lengthOK := metadata.structs["Symbol::_length"]
	bodyField, bodyOK := metadata.structs["Symbol::_body[0]"]
	if !bodyOK {
		bodyField, bodyOK = metadata.structs["Symbol::_body"]
	}
	if !lengthOK || !bodyOK {
		return "", errors.New("HotSpot Symbol layout is unavailable")
	}
	lengthRaw, err := memory.read(address+lengthField.offset, 2)
	if err != nil {
		return "", err
	}
	length := binary.LittleEndian.Uint16(lengthRaw)
	if length == 0 || length > maxHotSpotStringBytes {
		return "", errors.New("HotSpot Symbol length is invalid")
	}
	raw, err := memory.read(address+bodyField.offset, int(length))
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func objectSize(raw []byte, klass *klass,
	metadata *vmMeta, mirrorOopSizeOffset int, objectHeaderBytes uint64,
) (uint64, error) {
	layout := klass.layoutHelper
	if layout > 0 {
		slowBit := uint32(metadata.constants["Klass::_lh_instance_slow_path_bit"])
		if uint32(layout)&slowBit != 0 {
			if klass.name != "java/lang/Class" {
				return 0, fmt.Errorf("unsupported HotSpot slow-path instance %s",
					klass.name)
			}
			if mirrorOopSizeOffset <= 0 || mirrorOopSizeOffset+4 > len(raw) {
				return 0, errors.New("HotSpot class mirror size field is unavailable")
			}
			words := binary.LittleEndian.Uint32(
				raw[mirrorOopSizeOffset : mirrorOopSizeOffset+4])
			if words == 0 {
				return 0, errors.New("HotSpot class mirror size is zero")
			}
			return uint64(words) * uint64(metadata.constants["HeapWordSize"]), nil
		}
		bytes := uint64(uint32(layout) &^ slowBit)
		if bytes == 0 {
			return 0, errors.New("HotSpot instance size is zero")
		}
		return alignUp(bytes, metadata.alignment()), nil
	}
	if layout == 0 {
		return 0, errors.New("HotSpot neutral layout helper is unsupported")
	}
	headerShift := uint(metadata.constants["Klass::_lh_header_size_shift"])
	headerMask := uint32(metadata.constants["Klass::_lh_header_size_mask"])
	elementShift := uint(metadata.constants["Klass::_lh_log2_element_size_shift"])
	elementMask := uint32(metadata.constants["Klass::_lh_log2_element_size_mask"])
	headerBytes := uint64((uint32(layout) >> headerShift) & headerMask)
	logElementBytes := (uint32(layout) >> elementShift) & elementMask
	lengthOffset := int64(objectHeaderBytes)
	if value, ok := metadata.constants["arrayOopDesc_length_offset_in_bytes"]; ok {
		lengthOffset = value
	}
	if lengthOffset < 0 || lengthOffset+4 > int64(len(raw)) || logElementBytes > 8 {
		return 0, errors.New("HotSpot array layout is invalid")
	}
	length := binary.LittleEndian.Uint32(raw[lengthOffset : lengthOffset+4])
	bytes := headerBytes + (uint64(length) << logElementBytes)
	return alignUp(bytes, metadata.alignment()), nil
}

// humongousObjectSize reads only the one four-byte payload field needed by an
// array or java.lang.Class mirror. Ordinary instances are sized from Klass
// metadata alone, so humongous scanning never copies an arbitrary 4 KiB body.
func humongousObjectSize(memory processMemory, objectAddress uint64,
	raw []byte, class *klass, metadata *vmMeta, mirrorOopSizeOffset int,
	objectHeaderBytes uint64,
) (uint64, error) {
	layout := class.layoutHelper
	if layout > 0 {
		slowBit := uint32(metadata.constants["Klass::_lh_instance_slow_path_bit"])
		if uint32(layout)&slowBit == 0 {
			return objectSize(raw, class, metadata, mirrorOopSizeOffset,
				objectHeaderBytes)
		}
		if class.name != "java/lang/Class" || mirrorOopSizeOffset <= 0 {
			return 0, errors.New("HotSpot class mirror size field is unavailable")
		}
		address, ok := checkedAdd(objectAddress, uint64(mirrorOopSizeOffset))
		if !ok {
			return 0, errors.New("HotSpot class mirror size address overflows")
		}
		words, err := memory.uint32(address)
		if err != nil || words == 0 {
			return 0, errors.New("HotSpot class mirror size is unavailable")
		}
		return uint64(words) * uint64(metadata.constants["HeapWordSize"]), nil
	}
	if layout == 0 {
		return 0, errors.New("HotSpot neutral layout helper is unsupported")
	}
	lengthOffset := objectHeaderBytes
	if value, ok := metadata.constants["arrayOopDesc_length_offset_in_bytes"]; ok {
		lengthOffset = uint64(value)
	}
	address, ok := checkedAdd(objectAddress, lengthOffset)
	if !ok {
		return 0, errors.New("HotSpot array length address overflows")
	}
	length, err := memory.uint32(address)
	if err != nil {
		return 0, errors.New("HotSpot array length is unavailable")
	}
	headerShift := uint(metadata.constants["Klass::_lh_header_size_shift"])
	headerMask := uint32(metadata.constants["Klass::_lh_header_size_mask"])
	elementShift := uint(metadata.constants["Klass::_lh_log2_element_size_shift"])
	elementMask := uint32(metadata.constants["Klass::_lh_log2_element_size_mask"])
	headerBytes := uint64((uint32(layout) >> headerShift) & headerMask)
	logElementBytes := (uint32(layout) >> elementShift) & elementMask
	if logElementBytes > 8 {
		return 0, errors.New("HotSpot array layout is invalid")
	}
	return alignUp(headerBytes+(uint64(length)<<logElementBytes),
		metadata.alignment()), nil
}

func (encoding ptrEncoding) headerBytes() uint64 {
	if encoding.compressedKlass {
		return 12
	}
	return 16
}

func (encoding ptrEncoding) klassAddress(raw []byte) (uint64, bool) {
	if encoding.compressedKlass {
		if len(raw) < 12 {
			return 0, false
		}
		narrow := binary.LittleEndian.Uint32(raw[8:12])
		shifted := uint64(narrow) << encoding.klassShift
		address, valid := checkedAdd(encoding.klassBase, shifted)
		return address, narrow != 0 && valid
	}
	if len(raw) < 16 {
		return 0, false
	}
	address := binary.LittleEndian.Uint64(raw[8:16])
	return address, address != 0
}

func pointerEncoding(memory processMemory, metadata *vmMeta) (ptrEncoding, error) {
	encoding := ptrEncoding{compressedKlass: metadata.compressedKlass}
	if !encoding.compressedKlass {
		return encoding, nil
	}
	baseField := firstStruct(metadata, "CompressedKlassPointers::_base",
		"CompressedKlassPointers::_narrow_klass._base",
		"Universe::_narrow_klass._base")
	shiftField := firstStruct(metadata, "CompressedKlassPointers::_shift",
		"CompressedKlassPointers::_narrow_klass._shift",
		"Universe::_narrow_klass._shift")
	if !baseField.isStatic || !shiftField.isStatic {
		return ptrEncoding{}, unsupportedHotSpot(
			"compressed Klass pointer metadata is unavailable")
	}
	base, err := memory.uint64(baseField.address)
	if err != nil {
		return ptrEncoding{}, err
	}
	shift, err := memory.uint32(shiftField.address)
	if err != nil || shift > 16 {
		return ptrEncoding{}, unsupportedHotSpot(
			"compressed Klass shift is invalid")
	}
	encoding.klassBase = base
	encoding.klassShift = uint(shift)
	return encoding, nil
}

func mirrorSizeOffset(memory processMemory,
	metadata *vmMeta,
) (int, error) {
	field, ok := metadata.structs["java_lang_Class::_oop_size_offset"]
	if !ok || !field.isStatic || field.address == 0 {
		// Only java.lang.Class mirrors need this optional VMStruct. Other
		// classes remain safe to size and should still produce a histogram.
		return 0, nil
	}
	value, err := memory.uint32(field.address)
	if err != nil || value > 4096 {
		return 0, unsupportedHotSpot("class mirror size offset is invalid")
	}
	return int(value), nil
}

func normalizeClassName(name string) string {
	dimensions := 0
	for dimensions < len(name) && name[dimensions] == '[' {
		dimensions++
	}
	if dimensions == 0 {
		return strings.ReplaceAll(name, "/", ".")
	}
	descriptor := name[dimensions:]
	var base string
	switch descriptor {
	case "Z":
		base = "boolean"
	case "B":
		base = "byte"
	case "C":
		base = "char"
	case "S":
		base = "short"
	case "I":
		base = "int"
	case "J":
		base = "long"
	case "F":
		base = "float"
	case "D":
		base = "double"
	default:
		if strings.HasPrefix(descriptor, "L") && strings.HasSuffix(descriptor, ";") {
			base = strings.TrimSuffix(strings.TrimPrefix(descriptor, "L"), ";")
		} else {
			return name
		}
	}
	return strings.ReplaceAll(base, "/", ".") + strings.Repeat("[]", dimensions)
}

func sortObjects(objects []memsnap.ObjectAggregate) {
	sort.Slice(objects, func(i, j int) bool {
		if objects[i].ShallowBytes != objects[j].ShallowBytes {
			return objects[i].ShallowBytes > objects[j].ShallowBytes
		}
		if objects[i].Count != objects[j].Count {
			return objects[i].Count > objects[j].Count
		}
		return objects[i].TypeName < objects[j].TypeName
	})
}

func addClassSample(samples map[uint64]classSample, classAddress,
	count, bytes uint64,
) {
	sample := samples[classAddress]
	sample.count = memsnap.SaturatingAdd(sample.count, count)
	sample.bytes = memsnap.SaturatingAdd(sample.bytes, bytes)
	samples[classAddress] = sample
}

func estimateAggregates(classes map[uint64]*klass, statistics sampleStats,
	ordinaryBytes uint64,
) map[string]memsnap.ObjectAggregate {
	aggregates := make(map[string]memsnap.ObjectAggregate,
		len(statistics.ordinary)+len(statistics.humongous))
	className := func(classAddress uint64) (string, bool) {
		class := classes[classAddress]
		if class == nil {
			return "", false
		}
		return normalizeClassName(class.name), true
	}
	add := func(name string, count, bytes uint64) {
		aggregate := aggregates[name]
		aggregate.TypeName = name
		aggregate.Count = memsnap.SaturatingAdd(aggregate.Count, count)
		aggregate.ShallowBytes = memsnap.SaturatingAdd(aggregate.ShallowBytes, bytes)
		aggregates[name] = aggregate
	}
	for classAddress, sample := range statistics.ordinary {
		name, valid := className(classAddress)
		if !valid {
			continue
		}
		add(name,
			scaleEstimate(sample.count, ordinaryBytes,
				statistics.ordinarySampledBytes),
			scaleEstimate(sample.bytes, ordinaryBytes,
				statistics.ordinarySampledBytes))
	}
	for classAddress, sample := range statistics.humongous {
		name, valid := className(classAddress)
		if !valid {
			continue
		}
		add(name, sample.count, sample.bytes)
	}
	return aggregates
}

func scaleEstimate(observed, totalUsed, sampledUsed uint64) uint64 {
	if observed == 0 || totalUsed == 0 || sampledUsed == 0 {
		return observed
	}
	estimate := float64(observed) * float64(totalUsed) / float64(sampledUsed)
	result := clampFloatToUint64(math.Round(estimate))
	if result < observed {
		return observed
	}
	return result
}

func clampFloatToUint64(value float64) uint64 {
	if value <= 0 || math.IsNaN(value) {
		return 0
	}
	if value >= float64(^uint64(0)) || math.IsInf(value, 1) {
		return ^uint64(0)
	}
	return uint64(value)
}

func alignUp(value, alignment uint64) uint64 {
	return (value + alignment - 1) &^ (alignment - 1)
}
