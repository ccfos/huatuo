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
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

type klass struct {
	name         string
	layoutHelper int32
}

type klassHeader struct {
	address     uint64
	namePointer uint64
	layout      int32
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
	if err != nil {
		return nil, fmt.Errorf("read HotSpot Klass name pointer: %w", err)
	}
	if namePointer == 0 {
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
					raw[layoutField.offset-firstOffset:],
				)),
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
			decoded, err := decodeModifiedUTF8(name)
			if err != nil {
				continue
			}
			header := nameHeaders[index]
			resolved[header.address] = &klass{
				name: decoded, layoutHelper: header.layout,
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
	lengthAddress, valid := checkedAdd(address, lengthField.offset)
	if !valid {
		return "", errors.New("HotSpot Symbol length address overflows")
	}
	lengthRaw, err := memory.read(lengthAddress, 2)
	if err != nil {
		return "", err
	}
	length := binary.LittleEndian.Uint16(lengthRaw)
	if length == 0 || length > maxHotSpotStringBytes {
		return "", errors.New("HotSpot Symbol length is invalid")
	}
	bodyAddress, valid := checkedAdd(address, bodyField.offset)
	if !valid {
		return "", errors.New("HotSpot Symbol body address overflows")
	}
	raw, err := memory.read(bodyAddress, int(length))
	if err != nil {
		return "", err
	}
	name, err := decodeModifiedUTF8(raw)
	if err != nil {
		return "", fmt.Errorf("decode HotSpot Symbol: %w", err)
	}
	return name, nil
}

func decodeModifiedUTF8(raw []byte) (string, error) {
	if utf8.Valid(raw) {
		return string(raw), nil
	}
	result := make([]rune, 0, len(raw))
	for index := 0; index < len(raw); {
		code, size, err := modifiedUTF8Rune(raw[index:])
		if err != nil {
			return "", err
		}
		index += size
		if code >= 0xd800 && code <= 0xdbff && index < len(raw) {
			low, lowSize, lowErr := modifiedUTF8Rune(raw[index:])
			if lowErr == nil && low >= 0xdc00 && low <= 0xdfff {
				code = utf16.DecodeRune(code, low)
				index += lowSize
			}
		}
		if code >= 0xd800 && code <= 0xdfff {
			code = utf8.RuneError
		}
		result = append(result, code)
	}
	return string(result), nil
}

func modifiedUTF8Rune(raw []byte) (rune, int, error) {
	if len(raw) == 0 {
		return 0, 0, errors.New("modified UTF-8 is truncated")
	}
	switch value := raw[0]; {
	case value == 0:
		return 0, 0, errors.New("modified UTF-8 contains an embedded NUL")
	case value&0x80 == 0:
		return rune(value), 1, nil
	case value&0xe0 == 0xc0:
		if len(raw) < 2 || raw[1]&0xc0 != 0x80 {
			return 0, 0, errors.New("modified UTF-8 has a truncated two-byte sequence")
		}
		code := rune(value&0x1f)<<6 | rune(raw[1]&0x3f)
		if code < 0x80 && !(value == 0xc0 && raw[1] == 0x80) {
			return 0, 0, errors.New("modified UTF-8 has a noncanonical two-byte sequence")
		}
		return code, 2, nil
	case value&0xf0 == 0xe0:
		if len(raw) < 3 || raw[1]&0xc0 != 0x80 || raw[2]&0xc0 != 0x80 {
			return 0, 0, errors.New("modified UTF-8 has a truncated three-byte sequence")
		}
		code := rune(value&0x0f)<<12 |
			rune(raw[1]&0x3f)<<6 |
			rune(raw[2]&0x3f)
		if code < 0x800 {
			return 0, 0, errors.New("modified UTF-8 has a noncanonical three-byte sequence")
		}
		return code, 3, nil
	default:
		return 0, 0, errors.New("modified UTF-8 has an invalid byte")
	}
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
