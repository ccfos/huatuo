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

package golang

import (
	"debug/elf"
	"debug/gosym"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/arch/x86/x86asm"
)

const (
	maxMProfFlushCodeSize     = 512
	maxMemProfileRateCodeSize = 512
	initialMemProfileRate     = 512 * 1024
)

// lookupStrippedMBuckets recovers runtime.mbuckets when ELF symbols were
// removed by -ldflags=-s. The Go runtime still needs pclntab for stack
// unwinding, so runtime.mProf_FlushLocked can be located and its short machine
// code inspected without changing the victim's build flags.
func lookupStrippedMBuckets(file *elf.File, table *gosym.Table) (uint64, error) {
	if file.Machine != elf.EM_X86_64 {
		return 0, fmt.Errorf("%w: stripped %s binaries are unsupported",
			errMBucketsSymbolNotFound, file.Machine)
	}
	function := table.LookupFunc("runtime.mProf_FlushLocked")
	if function == nil || function.End <= function.Entry {
		return 0, fmt.Errorf("%w: runtime.mProf_FlushLocked is absent from pclntab",
			errMBucketsSymbolNotFound)
	}
	size := function.End - function.Entry
	if size > maxMProfFlushCodeSize {
		size = maxMProfFlushCodeSize
	}
	code, err := readVirtualRange(file, function.Entry, size)
	if err != nil {
		return 0, fmt.Errorf("%w: read runtime.mProf_FlushLocked: %w",
			errMBucketsSymbolNotFound, err)
	}
	address, err := decodeAMD64MBuckets(file, table, function.Entry, code)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", errMBucketsSymbolNotFound, err)
	}
	return address, nil
}

// lookupStrippedRate uses pclntab functions as anchors and accepts
// only one writable global initialized to the Go runtime default.
func lookupStrippedRate(file *elf.File,
	table *gosym.Table,
) (uint64, error) {
	if file.Machine != elf.EM_X86_64 {
		return 0, fmt.Errorf("runtime.MemProfileRate: stripped %s binaries are unsupported",
			file.Machine)
	}
	anchors := []string{
		"runtime.profilealloc", "runtime.nextSample",
		"runtime.nextSampleNoFP",
	}
	var failures []string
	for _, name := range anchors {
		function := table.LookupFunc(name)
		if function == nil || function.End <= function.Entry {
			continue
		}
		size := function.End - function.Entry
		if size > maxMemProfileRateCodeSize {
			size = maxMemProfileRateCodeSize
		}
		code, readErr := readVirtualRange(file, function.Entry, size)
		if readErr != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", name, readErr))
			continue
		}
		address, decodeErr := decodeAMD64Rate(file, function.Entry, code)
		if decodeErr == nil {
			return address, nil
		}
		failures = append(failures, fmt.Sprintf("%s: %v", name, decodeErr))
	}
	if len(failures) == 0 {
		return 0, errors.New("runtime.MemProfileRate: no pclntab anchor is available")
	}
	return 0, fmt.Errorf("runtime.MemProfileRate: %s", strings.Join(failures, "; "))
}

func decodeAMD64Rate(file *elf.File, entry uint64,
	code []byte,
) (uint64, error) {
	type rateLoad struct {
		address uint64
		reg     x86asm.Reg
		nextPC  uint64
	}
	var loads []rateLoad
	candidates := make(map[uint64]int)
	for offset := 0; offset < len(code); {
		instruction, err := x86asm.Decode(code[offset:], 64)
		if err != nil {
			return 0, fmt.Errorf("decode amd64 instruction at %#x: %w",
				entry+uint64(offset), err)
		}
		if instruction.Len == 0 {
			return 0, fmt.Errorf("decode amd64 instruction at %#x: zero-length instruction",
				entry+uint64(offset))
		}
		pc := entry + uint64(offset)
		nextPC := entry + uint64(offset+instruction.Len)
		if instruction.Op == x86asm.MOV && instruction.MemBytes == 8 {
			reg, regOK := instruction.Args[0].(x86asm.Reg)
			address, addressOK := ripRelativeArg(nextPC, instruction.Args[1])
			if regOK && addressOK && isWritableAddress(file, address) {
				loads = append(loads, rateLoad{address: address, reg: reg, nextPC: nextPC})
				if isInitialMemProfileRate(file, address) {
					candidates[address]++
				}
			}
		}
		// Go 1.18-1.19 initialize MemProfileRate through
		// defaultMemProfileRate, so stripped ELF files can leave the global in
		// zero-filled BSS. In runtime.profilealloc the value is loaded and then
		// compared with 1; requiring that local data-flow avoids accepting an
		// arbitrary zero-valued global.
		if instruction.Op == x86asm.CMP && instruction.Args[1] == x86asm.Imm(1) {
			if reg, ok := instruction.Args[0].(x86asm.Reg); ok {
				for index := len(loads) - 1; index >= 0; index-- {
					loaded := loads[index]
					if pc < loaded.nextPC || pc-loaded.nextPC > 16 {
						break
					}
					if loaded.reg == reg && isStaticallyZero(file, loaded.address) {
						candidates[loaded.address]++
						break
					}
				}
			}
		}
		offset += instruction.Len
	}
	if len(candidates) == 1 {
		for address := range candidates {
			return address, nil
		}
	}
	return 0, fmt.Errorf("instruction pattern is ambiguous (%d candidates)",
		len(candidates))
}

func isInitialMemProfileRate(file *elf.File, address uint64) bool {
	if !isWritableAddress(file, address) {
		return false
	}
	data, err := readVirtualRange(file, address, 8)
	return err == nil && file.ByteOrder.Uint64(data) == initialMemProfileRate
}

func isStaticallyZero(file *elf.File, address uint64) bool {
	if !isWritableAddress(file, address) {
		return false
	}
	for _, program := range file.Progs {
		if program.Type != elf.PT_LOAD || address < program.Vaddr ||
			program.Memsz < 8 || address-program.Vaddr > program.Memsz-8 {
			continue
		}
		offset := address - program.Vaddr
		if offset >= program.Filesz {
			return true
		}
		if program.Filesz < 8 || offset > program.Filesz-8 {
			return false
		}
		var data [8]byte
		_, err := program.ReadAt(data[:], int64(offset))
		return err == nil && file.ByteOrder.Uint64(data[:]) == 0
	}
	return false
}

func decodeAMD64MBuckets(
	file *elf.File,
	table *gosym.Table,
	entry uint64,
	code []byte,
) (uint64, error) {
	var (
		directLoads []uint64
		lastLEA     uint64
		lastLEAPC   uint64
	)
	for offset := 0; offset < len(code); {
		instruction, err := x86asm.Decode(code[offset:], 64)
		if err != nil {
			return 0, fmt.Errorf("decode amd64 instruction at %#x: %w", entry+uint64(offset), err)
		}
		if instruction.Len == 0 {
			return 0, fmt.Errorf("decode amd64 instruction at %#x: zero-length instruction",
				entry+uint64(offset))
		}
		pc := entry + uint64(offset)
		nextPC := pc + uint64(instruction.Len)

		if instruction.Op == x86asm.LEA && instruction.Args[0] == x86asm.RAX {
			if address, ok := ripRelativeArg(nextPC, instruction.Args[1]); ok &&
				isWritableAddress(file, address) {
				lastLEA = address
				lastLEAPC = pc
			}
		}
		if instruction.Op == x86asm.CALL && lastLEA != 0 && pc-lastLEAPC <= 16 {
			if target, ok := relativeArg(nextPC, instruction.Args[0]); ok {
				if called := table.PCToFunc(target); called != nil &&
					isAtomicPointerLoad(called.Name) {
					return lastLEA, nil
				}
			}
		}
		if instruction.Op == x86asm.MOV && instruction.MemBytes == 8 {
			if _, ok := instruction.Args[0].(x86asm.Reg); ok {
				if address, ok := ripRelativeArg(nextPC, instruction.Args[1]); ok &&
					isWritableAddress(file, address) {
					directLoads = appendUniqueAddress(directLoads, address)
				}
			}
		}
		offset += instruction.Len
	}
	if len(directLoads) == 1 {
		return directLoads[0], nil
	}
	return 0, fmt.Errorf("runtime.mbuckets instruction pattern is ambiguous (%d candidates)",
		len(directLoads))
}

func ripRelativeArg(nextPC uint64, argument x86asm.Arg) (uint64, bool) {
	memory, ok := argument.(x86asm.Mem)
	if !ok || memory.Base != x86asm.RIP || memory.Index != 0 {
		return 0, false
	}
	return addSignedOffset(nextPC, memory.Disp)
}

func relativeArg(nextPC uint64, argument x86asm.Arg) (uint64, bool) {
	delta, ok := argument.(x86asm.Rel)
	if !ok {
		return 0, false
	}
	return addSignedOffset(nextPC, int64(delta))
}

func addSignedOffset(base uint64, offset int64) (uint64, bool) {
	if offset >= 0 {
		value := base + uint64(offset)
		return value, value >= base
	}
	delta := uint64(-offset)
	if delta > base {
		return 0, false
	}
	return base - delta, true
}

func isWritableAddress(file *elf.File, address uint64) bool {
	for _, program := range file.Progs {
		if program.Type == elf.PT_LOAD && program.Flags&elf.PF_W != 0 &&
			program.Memsz >= 8 && address >= program.Vaddr &&
			address-program.Vaddr <= program.Memsz-8 {
			return true
		}
	}
	return false
}

func isAtomicPointerLoad(name string) bool {
	return strings.Contains(name, "atomic") && strings.HasSuffix(name, ").Load")
}

func appendUniqueAddress(addresses []uint64, address uint64) []uint64 {
	for _, existing := range addresses {
		if existing == address {
			return addresses
		}
	}
	return append(addresses, address)
}
