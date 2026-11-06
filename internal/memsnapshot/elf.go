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
	"fmt"

	"github.com/ccfos/huatuo/internal/elfmapping"
)

// FindELFLoadBias uses the selected device/inode-verified mapping so separate
// loads of the same file cannot borrow each other's relocation.
func FindELFLoadBias(file *elf.File, mappings []ProcMap, target *ProcMap) (uint64, error) {
	for index := range mappings {
		mapping := &mappings[index]
		if mapping.Start == target.Start && mapping.Offset == target.Offset && mapping.Inode == target.Inode &&
			mapping.DevMajor == target.DevMajor && mapping.DevMinor == target.DevMinor {
			return elfmapping.LoadBias(elfmapping.LoadSegments(file), target.Start, target.Offset, elfmapping.MappingFlags(target.Perms))
		}
	}
	return 0, fmt.Errorf("ELF mapping %#x at offset %#x has no matching device/inode identity", target.Start, target.Offset)
}
