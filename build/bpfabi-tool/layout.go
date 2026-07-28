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

package main

import (
	"fmt"
	"strconv"
	"strings"
	"unsafe"

	"github.com/cilium/ebpf/btf"
)

type typeLayout struct {
	size  uint32
	align uint32
}

func validateType(objectPath, name string, typ btf.Type) error {
	if _, err := canonicalType(typ, make(map[btf.Type]bool)); err != nil {
		return fmt.Errorf("object %q type %q: %w", objectPath, name, err)
	}
	if _, err := goTypeLayout(typ, make(map[btf.Type]bool)); err != nil {
		return fmt.Errorf("object %q type %q: %w", objectPath, name, err)
	}
	return nil
}

func canonicalType(typ btf.Type, active map[btf.Type]bool) (string, error) {
	typ = stripQualifiers(typ)
	if active[typ] {
		return "", fmt.Errorf("recursive type %q is unsupported", typ.TypeName())
	}
	active[typ] = true
	defer delete(active, typ)

	switch t := typ.(type) {
	case *btf.Int:
		if t.Size != 1 && t.Size != 2 && t.Size != 4 && t.Size != 8 {
			return "", fmt.Errorf("integer %q has unsupported size %d", t.Name, t.Size)
		}
		if t.Encoding == btf.Bool {
			return "", fmt.Errorf("boolean %q is unsupported", t.Name)
		}
		return fmt.Sprintf("int(%s,%d,%d)", t.Name, t.Size, t.Encoding), nil
	case *btf.Array:
		if t.Nelems == 0 {
			return "", fmt.Errorf("flexible or zero-length array is unsupported")
		}
		elem, err := canonicalType(t.Type, active)
		if err != nil {
			return "", fmt.Errorf("array element: %w", err)
		}
		return fmt.Sprintf("array(%d,%s)", t.Nelems, elem), nil
	case *btf.Struct:
		var b strings.Builder
		fmt.Fprintf(&b, "struct(%s,%d){", t.Name, t.Size)
		for _, member := range t.Members {
			if member.BitfieldSize != 0 {
				return "", fmt.Errorf(
					"field %q is a %d-bit bitfield",
					member.Name,
					member.BitfieldSize,
				)
			}
			if member.Offset%8 != 0 {
				return "", fmt.Errorf(
					"field %q offset %d is not byte-aligned",
					member.Name,
					member.Offset,
				)
			}
			field, err := canonicalType(member.Type, active)
			if err != nil {
				return "", fmt.Errorf("field %q: %w", member.Name, err)
			}
			fmt.Fprintf(&b, "%s@%d:%s;", member.Name, member.Offset, field)
		}
		b.WriteByte('}')
		return b.String(), nil
	case *btf.Typedef:
		target, err := canonicalType(t.Type, active)
		if err != nil {
			return "", fmt.Errorf("typedef %q: %w", t.Name, err)
		}
		return fmt.Sprintf("typedef(%s,%s)", t.Name, target), nil
	case *btf.Pointer:
		return "", fmt.Errorf("pointer is unsupported")
	case *btf.Union:
		return "", fmt.Errorf("union %q is unsupported", t.Name)
	default:
		return "", fmt.Errorf("btf kind %T is unsupported", typ)
	}
}

func goTypeLayout(typ btf.Type, active map[btf.Type]bool) (typeLayout, error) {
	typ = stripQualifiers(typ)
	if active[typ] {
		return typeLayout{}, fmt.Errorf("recursive type %q is unsupported", typ.TypeName())
	}
	active[typ] = true
	defer delete(active, typ)

	switch t := typ.(type) {
	case *btf.Int:
		align, err := integerAlign(t.Size)
		if err != nil {
			return typeLayout{}, err
		}
		return typeLayout{size: t.Size, align: align}, nil
	case *btf.Array:
		if t.Nelems == 0 {
			return typeLayout{}, fmt.Errorf("flexible or zero-length array is unsupported")
		}
		elem, err := goTypeLayout(t.Type, active)
		if err != nil {
			return typeLayout{}, fmt.Errorf("array element: %w", err)
		}
		return typeLayout{size: elem.size * t.Nelems, align: elem.align}, nil
	case *btf.Struct:
		return goStructLayout(t, active)
	case *btf.Typedef:
		return goTypeLayout(t.Type, active)
	default:
		return typeLayout{}, fmt.Errorf("btf kind %T has no supported go layout", typ)
	}
}

func goStructLayout(t *btf.Struct, active map[btf.Type]bool) (typeLayout, error) {
	var current uint32
	var previousEnd uint32
	maxAlign := uint32(1)

	for _, member := range t.Members {
		if member.BitfieldSize != 0 || member.Offset%8 != 0 {
			return typeLayout{}, fmt.Errorf("field %q has unsupported bit layout", member.Name)
		}
		offset := member.Offset.Bytes()
		if offset < previousEnd {
			return typeLayout{}, fmt.Errorf("field %q overlaps the previous field", member.Name)
		}
		current += offset - previousEnd

		field, err := goTypeLayout(member.Type, active)
		if err != nil {
			return typeLayout{}, fmt.Errorf("field %q: %w", member.Name, err)
		}
		if field.align > maxAlign {
			maxAlign = field.align
		}
		aligned := alignUp(current, field.align)
		if aligned != offset {
			return typeLayout{}, fmt.Errorf(
				"field %q go offset is %d, btf offset is %d",
				member.Name,
				aligned,
				offset,
			)
		}
		current = offset + field.size
		previousEnd = current
	}

	if previousEnd > t.Size {
		return typeLayout{}, fmt.Errorf("fields end at %d beyond type size %d", previousEnd, t.Size)
	}
	current += t.Size - previousEnd
	if size := alignUp(current, maxAlign); size != t.Size {
		return typeLayout{}, fmt.Errorf("go size is %d, btf size is %d", size, t.Size)
	}
	return typeLayout{size: t.Size, align: maxAlign}, nil
}

func integerAlign(size uint32) (uint32, error) {
	switch size {
	case 1:
		return uint32(unsafe.Alignof(uint8(0))), nil
	case 2:
		return uint32(unsafe.Alignof(uint16(0))), nil
	case 4:
		return uint32(unsafe.Alignof(uint32(0))), nil
	case 8:
		return uint32(unsafe.Alignof(uint64(0))), nil
	default:
		return 0, fmt.Errorf("integer size %d is unsupported", size)
	}
}

func alignUp(value, alignment uint32) uint32 {
	return (value + alignment - 1) &^ (alignment - 1)
}

func stripQualifiers(typ btf.Type) btf.Type {
	for {
		switch t := typ.(type) {
		case *btf.Const:
			typ = t.Type
		case *btf.Volatile:
			typ = t.Type
		case *btf.Restrict:
			typ = t.Type
		default:
			return typ
		}
	}
}

func offsetLiteral(offset btf.Bits) string {
	return strconv.FormatUint(uint64(offset.Bytes()), 10)
}
