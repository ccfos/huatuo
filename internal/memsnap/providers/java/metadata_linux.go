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
	"bytes"
	"encoding/binary"
	"fmt"
)

const (
	maxCachedMetadataBytes = 8 << 20
	defaultObjectAlignment = 8
	maxObjectAlignment     = 256
)

const (
	maxHotSpotTableEntries  = 16384
	maxHotSpotStringBytes   = 4096
	hotSpotTableReadEntries = 128
)

type vmStruct struct {
	typeString string
	isStatic   bool
	offset     uint64
	address    uint64
}

type vmType struct {
	superclass string
	size       uint64
}

type vmMeta struct {
	image             *vmImage
	structs           map[string]vmStruct
	types             map[string]vmType
	constants         map[string]int64
	stringCache       map[uint64]string
	retainedBytes     uint64
	klassBytes        uint64
	klassLimitReached bool
	objectAlignment   uint64
	compressedKlass   bool
}

type offsetField struct {
	name  string
	width int
}

const maxFlagNameBytes = 64

func loadMetadata(procRoot string, memory processMemory) (*vmMeta, error) {
	if err := memory.check(); err != nil {
		return nil, err
	}
	image, err := discoverVM(memory.ctx, procRoot, memory.pid)
	if err != nil {
		return nil, err
	}
	metadata := &vmMeta{
		image: image, structs: make(map[string]vmStruct),
		types: make(map[string]vmType), constants: make(map[string]int64),
		stringCache: make(map[uint64]string),
	}
	if err := metadata.loadStructs(memory); err != nil {
		return nil, err
	}
	metadata.image.vmRelease = metadata.readVMRelease(memory)
	if err := metadata.loadTypes(memory); err != nil {
		return nil, err
	}
	if err := metadata.loadRuntimeConfig(memory); err != nil {
		return nil, err
	}
	if err := metadata.loadConstants(memory); err != nil {
		return nil, err
	}
	if err := metadata.validate(); err != nil {
		return nil, err
	}
	return metadata, nil
}

func (m *vmMeta) validate() error {
	required := []string{
		"Universe::_collectedHeap", "G1CollectedHeap::_hrm",
		"G1HeapRegionTable::_base", "G1HeapRegionTable::_length",
		"Klass::_layout_helper", "Klass::_name", "Symbol::_length",
	}
	for _, name := range required {
		if _, ok := m.structs[name]; !ok {
			return unsupportedHotSpot("required metadata " + name + " is unavailable")
		}
	}
	if firstStruct(m, "G1HeapRegionManager::_regions",
		"HeapRegionManager::_regions").typeString == "" {
		return unsupportedHotSpot("region manager layout is unavailable")
	}
	if _, ok := m.structs["Symbol::_body[0]"]; !ok {
		if _, ok = m.structs["Symbol::_body"]; !ok {
			return unsupportedHotSpot("required metadata Symbol::_body is unavailable")
		}
	}
	for _, name := range []string{
		"HeapWordSize", "Klass::_lh_instance_slow_path_bit",
		"Klass::_lh_header_size_shift", "Klass::_lh_header_size_mask",
		"Klass::_lh_log2_element_size_shift",
		"Klass::_lh_log2_element_size_mask",
	} {
		if _, ok := m.constants[name]; !ok {
			return unsupportedHotSpot("required constant " + name + " is unavailable")
		}
	}
	constantInRange := func(name string, minimum, maximum int64) error {
		value := m.constants[name]
		if value < minimum || value > maximum {
			return unsupportedHotSpot(fmt.Sprintf(
				"constant %s=%d is outside [%d,%d]",
				name, value, minimum, maximum))
		}
		return nil
	}
	if err := constantInRange("HeapWordSize", 8, 8); err != nil {
		return err
	}
	for _, name := range []string{
		"Klass::_lh_header_size_shift",
		"Klass::_lh_log2_element_size_shift",
	} {
		if err := constantInRange(name, 0, 31); err != nil {
			return err
		}
	}
	for _, name := range []string{
		"Klass::_lh_header_size_mask",
		"Klass::_lh_log2_element_size_mask",
	} {
		if err := constantInRange(name, 1, 0xffff); err != nil {
			return err
		}
	}
	slowBit := m.constants["Klass::_lh_instance_slow_path_bit"]
	if slowBit <= 0 || uint64(slowBit)&(uint64(slowBit)-1) != 0 {
		return unsupportedHotSpot(
			"Klass slow-path bit is not a positive power of two")
	}
	if value, ok := m.constants["arrayOopDesc_length_offset_in_bytes"]; ok &&
		(value < 8 || value > 256) {
		return unsupportedHotSpot(
			"array length offset is outside the supported range")
	}
	return nil
}

func (m *vmMeta) readCString(memory processMemory, address uint64) (string, error) {
	if address == 0 {
		return "", nil
	}
	if value, ok := m.stringCache[address]; ok {
		return value, nil
	}
	value, err := memory.cstring(address)
	if err != nil {
		return "", err
	}
	if err := m.reserveMetadata(uint64(len(value)) + 32); err != nil {
		return "", err
	}
	m.stringCache[address] = value
	return value, nil
}

// Charge cache entries and table keys/values to one budget before retaining
// them. Shared strings and replaced entries are conservatively charged again;
// fixed entry allowances also bound map overhead for short or empty strings.
func (m *vmMeta) reserveMetadata(size uint64) error {
	if size > maxCachedMetadataBytes || m.retainedBytes > maxCachedMetadataBytes-size {
		return unsupportedHotSpot("HotSpot retained metadata exceeds safety limit")
	}
	m.retainedBytes += size
	return nil
}

func readTableBatch(memory processMemory, baseAddress, stride uint64, start int) ([]byte, int, error) {
	entries := hotSpotTableReadEntries
	if remaining := maxHotSpotTableEntries - start; remaining < entries {
		entries = remaining
	}
	data, err := memory.read(baseAddress+uint64(start)*stride, entries*int(stride))
	if err == nil {
		return data, entries, nil
	}
	if entries == 1 {
		return nil, 0, err
	}
	data, err = memory.read(baseAddress+uint64(start)*stride, int(stride))
	if err != nil {
		return nil, 0, err
	}
	return data, 1, nil
}

func walkTable(memory processMemory, name string, baseAddress,
	stride uint64, visit func([]byte) (bool, error),
) error {
	strideBytes := int(stride)
	for index := 0; index < maxHotSpotTableEntries; {
		batch, entries, err := readTableBatch(memory, baseAddress, stride,
			index)
		if err != nil {
			return fmt.Errorf("read HotSpot %s entry %d: %w", name, index, err)
		}
		for batchIndex := 0; batchIndex < entries; batchIndex++ {
			entry := batch[batchIndex*strideBytes : (batchIndex+1)*strideBytes]
			index++
			done, err := visit(entry)
			if err != nil || done {
				return err
			}
		}
	}
	return unsupportedHotSpot(fmt.Sprintf("%s table exceeds safety limit", name))
}

func (m *vmMeta) readVMRelease(memory processMemory) string {
	field, ok := m.structs["Abstract_VM_Version::_s_vm_release"]
	if !ok || !field.isStatic || field.address == 0 {
		return ""
	}
	address, err := memory.uint64(field.address)
	if err != nil || address == 0 {
		return ""
	}
	version, err := m.readCString(memory, address)
	if err != nil {
		return ""
	}
	return version
}

func (m *vmMeta) symbol(name string) (uint64, error) {
	address, ok := m.image.symbols[name]
	if !ok {
		return 0, unsupportedHotSpot("metadata symbol " + name + " is unavailable")
	}
	return address, nil
}

func (m *vmMeta) value(memory processMemory, name string) (uint64, error) {
	address, err := m.symbol(name)
	if err != nil {
		return 0, err
	}
	return memory.uint64(address)
}

func validateOffsets(offsets map[string]uint64, stride int,
	fields []offsetField,
) error {
	for _, field := range fields {
		offset, ok := offsets[field.name]
		if !ok {
			return unsupportedHotSpot(
				"metadata offset " + field.name + " is unavailable")
		}
		if offset > uint64(stride) || offset+uint64(field.width) > uint64(stride) {
			return unsupportedHotSpot(fmt.Sprintf(
				"metadata offset %s=%d with width %d exceeds stride %d",
				field.name, offset, field.width, stride))
		}
	}
	return nil
}

func (m *vmMeta) loadStructs(memory processMemory) error {
	basePointer, err := m.value(memory, "gHotSpotVMStructs")
	if err != nil {
		return err
	}
	stride, err := m.value(memory, "gHotSpotVMStructEntryArrayStride")
	if err != nil {
		return err
	}
	if stride == 0 || stride > 256 {
		return unsupportedHotSpot("VMStruct stride is invalid")
	}
	offsets := make(map[string]uint64)
	for _, name := range []string{"TypeName", "FieldName", "TypeString", "IsStatic", "Offset", "Address"} {
		offset, offsetErr := m.value(memory, "gHotSpotVMStructEntry"+name+"Offset")
		if offsetErr != nil {
			return offsetErr
		}
		offsets[name] = offset
	}
	strideBytes := int(stride)
	if err := validateOffsets(offsets, strideBytes, []offsetField{
		{name: "TypeName", width: 8},
		{name: "FieldName", width: 8},
		{name: "TypeString", width: 8},
		{name: "IsStatic", width: 4},
		{name: "Offset", width: 8},
		{name: "Address", width: 8},
	}); err != nil {
		return err
	}
	return walkTable(memory, "VMStruct", basePointer, stride,
		func(entry []byte) (bool, error) {
			typePointer := binary.LittleEndian.Uint64(entry[offsets["TypeName"]:])
			if typePointer == 0 {
				return true, nil
			}
			fieldPointer := binary.LittleEndian.Uint64(entry[offsets["FieldName"]:])
			typeStringPointer := binary.LittleEndian.Uint64(entry[offsets["TypeString"]:])
			staticValue := binary.LittleEndian.Uint32(entry[offsets["IsStatic"]:])
			offset := binary.LittleEndian.Uint64(entry[offsets["Offset"]:])
			address := binary.LittleEndian.Uint64(entry[offsets["Address"]:])
			typeName, readErr := m.readCString(memory, typePointer)
			if readErr != nil {
				return false, readErr
			}
			fieldName, readErr := m.readCString(memory, fieldPointer)
			if readErr != nil {
				return false, readErr
			}
			typeString, readErr := m.readCString(memory, typeStringPointer)
			if readErr != nil {
				return false, readErr
			}
			if err := m.reserveMetadata(uint64(len(typeName)+2+len(fieldName)+len(typeString)) + 96); err != nil {
				return false, err
			}
			m.structs[typeName+"::"+fieldName] = vmStruct{
				typeString: typeString,
				isStatic:   staticValue != 0, offset: offset, address: address,
			}
			return false, nil
		},
	)
}

func (m *vmMeta) loadTypes(memory processMemory) error {
	basePointer, err := m.value(memory, "gHotSpotVMTypes")
	if err != nil {
		return err
	}
	stride, err := m.value(memory, "gHotSpotVMTypeEntryArrayStride")
	if err != nil {
		return err
	}
	if stride == 0 || stride > 256 {
		return unsupportedHotSpot("VMType stride is invalid")
	}
	offsets := make(map[string]uint64)
	for _, name := range []string{"TypeName", "SuperclassName", "Size"} {
		offset, offsetErr := m.value(memory, "gHotSpotVMTypeEntry"+name+"Offset")
		if offsetErr != nil {
			return offsetErr
		}
		offsets[name] = offset
	}
	strideBytes := int(stride)
	if err := validateOffsets(offsets, strideBytes, []offsetField{
		{name: "TypeName", width: 8},
		{name: "SuperclassName", width: 8},
		{name: "Size", width: 8},
	}); err != nil {
		return err
	}
	return walkTable(memory, "VMType", basePointer, stride,
		func(entry []byte) (bool, error) {
			namePointer := binary.LittleEndian.Uint64(entry[offsets["TypeName"]:])
			if namePointer == 0 {
				return true, nil
			}
			superPointer := binary.LittleEndian.Uint64(entry[offsets["SuperclassName"]:])
			name, readErr := m.readCString(memory, namePointer)
			if readErr != nil {
				return false, readErr
			}
			superclass, readErr := m.readCString(memory, superPointer)
			if readErr != nil {
				return false, readErr
			}
			if err := m.reserveMetadata(uint64(len(name)+len(superclass)) + 64); err != nil {
				return false, err
			}
			size := binary.LittleEndian.Uint64(entry[offsets["Size"]:])
			m.types[name] = vmType{superclass: superclass, size: size}
			return false, nil
		},
	)
}

func (m *vmMeta) loadConstants(memory processMemory) error {
	prefix := "gHotSpotVMIntConstantEntry"
	tableSymbol := "gHotSpotVMIntConstants"
	basePointer, err := m.value(memory, tableSymbol)
	if err != nil {
		return err
	}
	stride, err := m.value(memory, prefix+"ArrayStride")
	if err != nil {
		return err
	}
	if stride == 0 || stride > 128 {
		return unsupportedHotSpot("constant table stride is invalid")
	}
	nameOffset, err := m.value(memory, prefix+"NameOffset")
	if err != nil {
		return err
	}
	valueOffset, err := m.value(memory, prefix+"ValueOffset")
	if err != nil {
		return err
	}
	strideBytes := int(stride)
	if nameOffset > uint64(strideBytes) || nameOffset+8 > uint64(strideBytes) {
		return unsupportedHotSpot("constant name offset exceeds stride")
	}
	if valueOffset > uint64(strideBytes) ||
		valueOffset+4 > uint64(strideBytes) {
		return unsupportedHotSpot("constant value offset exceeds stride")
	}
	return walkTable(memory, "VMIntConstant", basePointer, stride,
		func(entry []byte) (bool, error) {
			namePointer := binary.LittleEndian.Uint64(entry[nameOffset:])
			if namePointer == 0 {
				return true, nil
			}
			name, readErr := m.readCString(memory, namePointer)
			if readErr != nil {
				return false, readErr
			}
			if err := m.reserveMetadata(uint64(len(name)) + 48); err != nil {
				return false, err
			}
			value := binary.LittleEndian.Uint32(entry[valueOffset:])
			m.constants[name] = int64(int32(value))
			return false, nil
		},
	)
}

// Both humongous and ordinary scans share this retained Klass budget.
func (m *vmMeta) cacheKlass(classes map[uint64]*klass, address uint64, class *klass) bool {
	if classes[address] != nil {
		return true
	}
	cost := uint64(len(class.name)) + 128
	if len(classes) >= maxCachedKlasses || cost > maxCachedMetadataBytes ||
		m.klassBytes > maxCachedMetadataBytes-cost {
		m.klassLimitReached = true
		return false
	}
	m.klassBytes += cost
	classes[address] = class
	return true
}

func (m *vmMeta) alignment() uint64 {
	if m != nil && m.objectAlignment != 0 {
		return m.objectAlignment
	}
	return defaultObjectAlignment
}

// loadRuntimeConfig reads the active values from HotSpot's SA-visible flag
// table. VMStruct entries alone only prove that G1 was compiled into libjvm;
// they do not prove that the target is currently using G1.
func (m *vmMeta) loadRuntimeConfig(memory processMemory) error {
	addresses, err := m.runtimeFlagAddresses(memory, map[string]struct{}{
		"UseG1GC": {}, "UseCompressedClassPointers": {},
		"ObjectAlignmentInBytes":  {},
		"UseCompactObjectHeaders": {},
	})
	if err != nil {
		return err
	}
	useG1Address, ok := addresses["UseG1GC"]
	if !ok {
		return unsupportedHotSpot("UseG1GC flag is unavailable")
	}
	useG1, err := readFlagBool(memory, useG1Address)
	if err != nil {
		return fmt.Errorf("read HotSpot UseG1GC flag: %w", err)
	}
	if !useG1 {
		return unsupportedHotSpot("target is not using G1")
	}
	if compactAddress, exists := addresses["UseCompactObjectHeaders"]; exists {
		compact, readErr := readFlagBool(memory, compactAddress)
		if readErr != nil {
			return fmt.Errorf("read HotSpot UseCompactObjectHeaders flag: %w", readErr)
		}
		if compact {
			return unsupportedHotSpot("compact object headers are enabled")
		}
	}
	compressedAddress, ok := addresses["UseCompressedClassPointers"]
	if !ok {
		return unsupportedHotSpot("UseCompressedClassPointers flag is unavailable")
	}
	m.compressedKlass, err = readFlagBool(memory, compressedAddress)
	if err != nil {
		return fmt.Errorf("read HotSpot UseCompressedClassPointers flag: %w", err)
	}
	alignmentAddress, ok := addresses["ObjectAlignmentInBytes"]
	if !ok {
		return unsupportedHotSpot("ObjectAlignmentInBytes flag is unavailable")
	}
	alignmentValue, err := memory.uint32(alignmentAddress)
	if err != nil {
		return fmt.Errorf("read HotSpot ObjectAlignmentInBytes flag: %w", err)
	}
	alignment := uint64(alignmentValue)
	if alignment < defaultObjectAlignment || alignment > maxObjectAlignment ||
		alignment&(alignment-1) != 0 {
		return unsupportedHotSpot(fmt.Sprintf(
			"ObjectAlignmentInBytes=%d is unsupported", alignment))
	}
	m.objectAlignment = alignment
	return nil
}

func readFlagBool(memory processMemory, address uint64) (bool, error) {
	raw, err := memory.read(address, 1)
	return len(raw) == 1 && raw[0] != 0, err
}

func (m *vmMeta) runtimeFlagAddresses(memory processMemory,
	wanted map[string]struct{},
) (map[string]uint64, error) {
	flagType := "JVMFlag"
	if _, ok := m.structs[flagType+"::_name"]; !ok {
		flagType = "Flag"
	}
	nameField, nameOK := m.structs[flagType+"::_name"]
	addressField, addressOK := m.structs[flagType+"::_addr"]
	flagsField, flagsOK := m.structs[flagType+"::flags"]
	countField, countOK := m.structs[flagType+"::numFlags"]
	stride := m.types[flagType].size
	if !nameOK || !addressOK || !flagsOK || !countOK ||
		!flagsField.isStatic || !countField.isStatic || stride == 0 || stride > 256 ||
		nameField.offset+8 > stride || addressField.offset+8 > stride {
		return nil, unsupportedHotSpot("HotSpot VM flag table layout is unavailable")
	}
	base, err := memory.uint64(flagsField.address)
	if err != nil {
		return nil, fmt.Errorf("read HotSpot VM flag table address: %w", err)
	}
	count, err := memory.uint64(countField.address)
	if err != nil {
		return nil, fmt.Errorf("read HotSpot VM flag count: %w", err)
	}
	if base == 0 || count == 0 || count > maxHotSpotTableEntries {
		return nil, unsupportedHotSpot("HotSpot VM flag table bounds are invalid")
	}

	found := make(map[string]uint64, len(wanted))
	for begin := uint64(0); begin < count && len(found) < len(wanted); begin += maxReadIOVs {
		entries := min(uint64(maxReadIOVs), count-begin)
		batchAddress, valid := checkedAdd(base, begin*stride)
		if !valid {
			return nil, unsupportedHotSpot("HotSpot VM flag table address overflows")
		}
		raw, readErr := memory.read(batchAddress, int(entries*stride))
		if readErr != nil {
			return nil, fmt.Errorf("read HotSpot VM flag table: %w", readErr)
		}
		type flagRecord struct {
			address uint64
			name    memoryRange
		}
		records := make([]flagRecord, 0, entries)
		for index := uint64(0); index < entries; index++ {
			entry := raw[index*stride : (index+1)*stride]
			namePointer := binary.LittleEndian.Uint64(entry[nameField.offset:])
			valueAddress := binary.LittleEndian.Uint64(entry[addressField.offset:])
			if namePointer != 0 && valueAddress != 0 {
				records = append(records, flagRecord{
					address: valueAddress,
					name: memoryRange{
						address: namePointer,
						size:    maxFlagNameBytes,
					},
				})
			}
		}
		ranges := make([]memoryRange, len(records))
		for index := range records {
			ranges[index] = records[index].name
		}
		names, readErr := memory.readv(ranges)
		if readErr != nil {
			return nil, fmt.Errorf("read HotSpot VM flag names: %w", readErr)
		}
		for index, rawName := range names {
			if end := bytes.IndexByte(rawName, 0); end >= 0 {
				name := string(rawName[:end])
				if _, match := wanted[name]; match {
					found[name] = records[index].address
				}
			}
		}
	}
	return found, nil
}
