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
)

const maxJavaObjectBytes = 1 << 40

type ptrEncoding struct {
	compressedKlass bool
	klassBase       uint64
	klassShift      uint
}

func objectSize(raw []byte, klass *klass,
	metadata *vmMeta, mirrorOopSizeOffset int, objectHeaderBytes uint64,
) (uint64, error) {
	layout := klass.layoutHelper
	if layout > 0 {
		slowBit := uint32(metadata.constants["Klass::_lh_instance_slow_path_bit"])
		// Slow allocation also applies to ordinary fixed-size classes, such as
		// finalizable instances. Only Class mirrors store their size in the object.
		if uint32(layout)&slowBit != 0 && klass.name == "java/lang/Class" {
			if mirrorOopSizeOffset <= 0 || mirrorOopSizeOffset+4 > len(raw) {
				return 0, errors.New("HotSpot class mirror size field is unavailable")
			}
			words := binary.LittleEndian.Uint32(
				raw[mirrorOopSizeOffset : mirrorOopSizeOffset+4],
			)
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
		if uint32(layout)&slowBit == 0 || class.name != "java/lang/Class" {
			return objectSize(raw, class, metadata, mirrorOopSizeOffset,
				objectHeaderBytes)
		}
		if mirrorOopSizeOffset <= 0 {
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
			"compressed Klass pointer metadata is unavailable",
		)
	}
	base, err := memory.uint64(baseField.address)
	if err != nil {
		return ptrEncoding{}, err
	}
	shift, err := memory.uint32(shiftField.address)
	if err != nil || shift > 16 {
		return ptrEncoding{}, unsupportedHotSpot(
			"compressed Klass shift is invalid",
		)
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

func alignUp(value, alignment uint64) uint64 {
	return (value + alignment - 1) &^ (alignment - 1)
}
