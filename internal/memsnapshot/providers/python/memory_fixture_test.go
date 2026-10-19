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

package python

import (
	"encoding/binary"
)

type sparseMemory map[uint64]byte

func (m sparseMemory) read(address uint64, size int) ([]byte, error) {
	result := make([]byte, size)
	_ = m.readInto(address, result)
	return result, nil
}

func (m sparseMemory) readInto(address uint64, destination []byte) error {
	for offset := range destination {
		destination[offset] = m[address+uint64(offset)]
	}
	return nil
}

func (m sparseMemory) put(address uint64, data []byte) {
	for offset, value := range data {
		m[address+uint64(offset)] = value
	}
}

func (m sparseMemory) put32(address uint64, value uint32) {
	raw := make([]byte, 4)
	binary.LittleEndian.PutUint32(raw, value)
	m.put(address, raw)
}
