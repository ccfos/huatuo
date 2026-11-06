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

package symbol

import (
	"context"
	"debug/elf"
	"fmt"
)

// Function offsets are relative to runtime.text, not ELF .text: external
// linkers can prepend C code. Go 1.26 leaves pcHeader.textStart zero; recover
// the base from matching moduledata instead of guessing the section address.
func goTextStart(ctx context.Context, file *elf.File, pcln []byte, pclnAddr, maxBytes uint64) (uint64, uint64, error) {
	base := goPCLNWord(pcln, 2, file)
	funcs := pcln[goPCLNWord(pcln, 7, file):]
	nfunc := goPCLNWord(pcln, 0, file)
	first := uint64(file.ByteOrder.Uint32(funcs))
	last := uint64(file.ByteOrder.Uint32(funcs[nfunc*8:]))
	validBase := func(base uint64) bool {
		if base == 0 || first >= last || base > ^uint64(0)-last {
			return false
		}
		for _, section := range file.Sections {
			if section.Flags&elf.SHF_EXECINSTR != 0 && base >= section.Addr &&
				base-section.Addr <= section.Size && last <= section.Size-(base-section.Addr) {
				return true
			}
		}
		return false
	}
	if base != 0 {
		if !validBase(base) {
			return 0, 0, fmt.Errorf("Go pcHeader text base is outside executable sections")
		}
		return base, 0, nil
	}
	if pclnAddr > ^uint64(0)-uint64(len(pcln)) {
		return 0, 0, fmt.Errorf("Go pclntab address overflows")
	}
	section := file.Section(".go.module")
	if section == nil {
		section = file.Section(".noptrdata")
	}
	if section == nil {
		return 0, 0, fmt.Errorf("Go runtime.text cannot be recovered without moduledata")
	}
	if section.Flags&elf.SHF_COMPRESSED != 0 || section.Size > maxBytes {
		return 0, 0, fmt.Errorf("%w: Go moduledata exceeds metadata budget", errELFSymbolLimit)
	}
	data, err := section.Data()
	if err != nil {
		return 0, 0, err
	}
	size := int(pcln[7])
	for offset := 0; offset+24*size <= len(data); offset += size {
		if offset%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return 0, 0, err
			}
		}
		word := func(index int) uint64 {
			if size == 4 {
				return uint64(file.ByteOrder.Uint32(data[offset+index*size:]))
			}
			return file.ByteOrder.Uint64(data[offset+index*size:])
		}
		if word(0) != pclnAddr || word(16) != pclnAddr+goPCLNWord(pcln, 7, file) || word(17) != nfunc+1 {
			continue
		}
		matched := true
		for index := 0; index < 5; index++ {
			if word(1+index*3) != pclnAddr+goPCLNWord(pcln, 3+index, file) {
				matched = false
				break
			}
		}
		text := word(22)
		if !matched || !validBase(text) || word(20) != text+first || word(21) != text+last {
			continue
		}
		if base != 0 && base != text {
			return 0, 0, fmt.Errorf("ambiguous Go moduledata text base")
		}
		base = text
	}
	if base == 0 {
		return 0, 0, fmt.Errorf("Go runtime.text not found in validated moduledata")
	}
	return base, section.Size, nil
}
