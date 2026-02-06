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

package cpuutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUint32ToBytes(t *testing.T) {
	tests := []struct {
		name     string
		input    []uint32
		expected []byte
	}{
		{
			name:     "multiple values",
			input:    []uint32{0x12345678, 0xABCDEF00, 0x00000001},
			expected: []byte{0x78, 0x56, 0x34, 0x12, 0x00, 0xEF, 0xCD, 0xAB, 0x01, 0x00, 0x00, 0x00},
		},
		{
			name:     "empty input",
			input:    []uint32{},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := uint32ToBytes(tt.input...)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestKVMSig(t *testing.T) {
	// KVMSig calls Cpuid which is implemented in assembly
	// We can't mock CPUID results, but we can verify the function runs
	origFn := CPUFn
	CPUFn = func(a, b uint32) (uint32, uint32, uint32, uint32) {
		return 0, 0, 0, 0
	}
	defer func() { CPUFn = origFn }()
	sig := KVMSig()
	assert.Equal(t, false, sig)
	CPUFn = func(a, b uint32) (uint32, uint32, uint32, uint32) {
		ebx := uint32('K') | uint32('V')<<8 | uint32('M')<<16 | uint32('K')<<24 // "KVMK"
		ecx := uint32('V') | uint32('M')<<8 | uint32('K')<<16 | uint32('V')<<24 // "VMKV"
		edx := uint32('M')                                                      // "M"
		return 0, ebx, ecx, edx
	}
	sig = KVMSig()
	assert.Equal(t, true, sig)
}
