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
)

// FindELFLoadBias tries load segments in ELF order because the first segment
// may not have a matching process mapping.
func FindELFLoadBias(file *elf.File, mappings []ProcMap, target *ProcMap) (uint64, error) {
	pageSize := uint64(os.Getpagesize())
	for _, program := range file.Progs {
		if program.Type != elf.PT_LOAD {
			continue
		}
		loadOffset := program.Off &^ (pageSize - 1)
		loadAddress := program.Vaddr &^ (pageSize - 1)
		if bias, err := FindLoadBias(mappings, target, loadOffset, loadAddress); err == nil {
			return bias, nil
		}
	}
	return 0, errors.New("ELF load bias not found")
}
