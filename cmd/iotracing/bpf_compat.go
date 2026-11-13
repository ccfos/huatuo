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

// Select request readers before opening tracing resources. Keep BTF loading
// separate from layout parsing so controlled fixtures exercise the same rules.
package main

import (
	"fmt"

	"github.com/cilium/ebpf/btf"
)

func requestPartIsBlockDevice() (bool, error) {
	kernel, err := btf.LoadKernelSpec()
	if err != nil {
		return false, fmt.Errorf("check request.part layout: load kernel BTF: %w", err)
	}
	return requestPartIsBlockDeviceFromSpec(kernel)
}

// Unrelated legacy types in kernel BTF must not affect device attribution.
func requestPartIsBlockDeviceFromSpec(spec *btf.Spec) (bool, error) {
	var request *btf.Struct
	if err := spec.TypeByName("request", &request); err != nil {
		return false, fmt.Errorf("unsupported request.part layout: find request: %w", err)
	}
	part, ok := btf.As[*btf.Pointer](requestLayoutMember(request, "part"))
	if !ok || part.Target == nil {
		return false, fmt.Errorf("unsupported request.part layout: part is not a typed pointer")
	}
	target := btf.UnderlyingType(part.Target)
	switch target.TypeName() {
	case "hd_struct":
		if requestLayoutMember(requestLayoutMember(target, "__dev"), "devt") != nil {
			return false, nil
		}
	case "block_device":
		if requestLayoutMember(target, "bd_dev") != nil {
			return true, nil
		}
	default:
		return false, fmt.Errorf("unsupported request.part layout: points to %s", target.TypeName())
	}
	return false, fmt.Errorf("unsupported request.part layout: device-number field missing in %s", target.TypeName())
}

func requestLayoutMember(typ btf.Type, name string) btf.Type {
	structure, ok := btf.As[*btf.Struct](typ)
	if !ok || structure == nil {
		return nil
	}
	for _, member := range structure.Members {
		if member.Name == name {
			return member.Type
		}
	}
	return nil
}
