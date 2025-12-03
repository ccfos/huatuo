// Copyright 2025 The HuaTuo Authors
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

import "huatuo-bamai/internal/utils/bytesutil"

// Cpuid aims to get the content of the CPUID register
func Cpuid(arg1, arg2 uint32) (eax, ebx, ecx, edx uint32) // implemented in cpuidlow_amd64.s

func uint32ToBytes(args ...uint32) []byte {
	var result []byte

	for _, arg := range args {
		result = append(result,
			byte((arg)&0xFF),
			byte((arg>>8)&0xFF),
			byte((arg>>16)&0xFF),
			byte((arg>>24)&0xFF))
	}

	return result
}

// KVMSig reports whether the KVM_CPUID_SIGNATURE is 'KVMKVMKVM'
func KVMSig() bool {
	// function: KVM_CPUID_SIGNATURE (0x40000000)
	_, ebx, ecx, edx := Cpuid(0x40000000, 0)

	sig := bytesutil.ToString(uint32ToBytes(ebx, ecx, edx))

	return sig == "KVMKVMKVM"
}
