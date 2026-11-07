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

package memsnapshot

import (
	"debug/elf"
	"errors"
	"os"
	"strings"
)

// FindELFLoadBias relates a file mapping to its load segment. A mapping may
// begin inside the segment after its permissions have been split by mprotect.
func FindELFLoadBias(file *elf.File, mappings []ProcMap, target *ProcMap) (uint64, error) {
	pageSize := uint64(os.Getpagesize())
	for _, program := range file.Progs {
		if program.Type != elf.PT_LOAD || program.Filesz == 0 {
			continue
		}
		loadOffset := program.Off &^ (pageSize - 1)
		loadAddress := program.Vaddr &^ (pageSize - 1)
		for _, mapping := range mappings {
			if mapping.Inode != target.Inode || mapping.DevMajor != target.DevMajor ||
				mapping.DevMinor != target.DevMinor || mapping.Offset < loadOffset {
				continue
			}
			if strings.Contains(mapping.Perms, "x") && program.Flags&elf.PF_X == 0 {
				continue
			}
			offset := mapping.Offset - loadOffset
			leading := program.Off - loadOffset
			if offset >= leading && offset-leading >= program.Filesz {
				continue
			}
			if offset > ^uint64(0)-loadAddress || mapping.Start < loadAddress+offset {
				continue
			}
			return mapping.Start - loadAddress - offset, nil
		}
	}
	return 0, errors.New("ELF load bias not found")
}
