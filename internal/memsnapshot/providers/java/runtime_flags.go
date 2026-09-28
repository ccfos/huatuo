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

const maxFlagNameBytes = 64

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
			"ObjectAlignmentInBytes=%d is unsupported", alignment,
		))
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
		!flagsField.isStatic || !countField.isStatic || stride < 8 || stride > 256 ||
		nameField.offset > stride-8 || addressField.offset > stride-8 {
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
						size:    pageReadSize(namePointer, maxFlagNameBytes),
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
			end := bytes.IndexByte(rawName, 0)
			if end < 0 && len(rawName) < maxFlagNameBytes {
				address, valid := checkedAdd(records[index].name.address, uint64(len(rawName)))
				if !valid {
					return nil, unsupportedHotSpot("VM flag name address overflows")
				}
				tail, err := memory.read(address, maxFlagNameBytes-len(rawName))
				if err != nil {
					return nil, fmt.Errorf("read HotSpot VM flag name: %w", err)
				}
				rawName = append(rawName, tail...)
				end = bytes.IndexByte(rawName, 0)
			}
			if end >= 0 {
				name := string(rawName[:end])
				if _, match := wanted[name]; match {
					found[name] = records[index].address
				}
			}
		}
	}
	return found, nil
}
