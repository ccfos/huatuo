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

package elfmapping

import (
	"debug/elf"
	"fmt"
	"os"
)

// LoadSegments retains only headers, not readers tied to an open ELF file.
func LoadSegments(file *elf.File) []elf.ProgHeader {
	var segments []elf.ProgHeader
	for _, program := range file.Progs {
		if program.Type == elf.PT_LOAD {
			segments = append(segments, program.ProgHeader)
		}
	}
	return segments
}

// LoadBias uses the selected mapping itself, not another mapping with the same
// pathname or inode: one file can be loaded more than once in a process.
// Callers select the mapping by address or device/inode identity.
// Required permissions distinguish RX/RW segments sharing a file page. Removed
// permissions (such as write protection after RELRO) do not exclude a segment.
func LoadBias(segments []elf.ProgHeader, mappingStart, mappingOffset uint64, required elf.ProgFlag) (uint64, error) {
	pageMask := uint64(os.Getpagesize() - 1)
	if mappingStart&pageMask != 0 || mappingOffset&pageMask != 0 {
		return 0, fmt.Errorf("ELF mapping start %#x and offset %#x must be page-aligned", mappingStart, mappingOffset)
	}
	var bias uint64
	found := false
	for index := range segments {
		segment := &segments[index]
		if segment.Type != elf.PT_LOAD || segment.Filesz == 0 || segment.Off&pageMask != segment.Vaddr&pageMask {
			continue
		}
		if segment.Flags&required != required {
			continue
		}
		offset := segment.Off &^ pageMask
		address := segment.Vaddr &^ pageMask
		if segment.Off > ^uint64(0)-segment.Filesz || mappingOffset < offset {
			continue
		}
		// Include the final partial file page, including a mapping split by
		// mprotect (for example GNU RELRO), without rounding past uint64.
		lastPage := (segment.Off + segment.Filesz - 1) &^ pageMask
		if mappingOffset > lastPage {
			continue
		}
		delta := mappingOffset - offset
		if address > ^uint64(0)-delta || mappingStart < address+delta {
			continue
		}
		candidate := mappingStart - (address + delta)
		if found && candidate != bias {
			return 0, fmt.Errorf("ambiguous ELF load bias for mapping %#x at offset %#x", mappingStart, mappingOffset)
		}
		bias, found = candidate, true
	}
	if !found {
		return 0, fmt.Errorf("no ELF load segment matches mapping %#x at offset %#x", mappingStart, mappingOffset)
	}
	return bias, nil
}

// MappingFlags converts proc maps permissions to the flags a load segment must
// provide; private/shared mapping policy is unrelated to ELF permissions.
func MappingFlags(permissions string) elf.ProgFlag {
	var flags elf.ProgFlag
	for _, permission := range permissions {
		switch permission {
		case 'r':
			flags |= elf.PF_R
		case 'w':
			flags |= elf.PF_W
		case 'x':
			flags |= elf.PF_X
		}
	}
	return flags
}
