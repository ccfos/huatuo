// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// IO collectors share operation labels and hash-allocator evidence without
// depending on another collector.
package collector

import (
	"fmt"
	"github.com/cilium/ebpf/btf"
)

func ioOperationName(operation uint32) (string, bool) {
	switch operation {
	case 0: // REQ_OP_READ
		return "read", true
	case 1: // REQ_OP_WRITE
		return "write", true
	default:
		return "", false
	}
}

// The ordinary HASH layout also backs PERCPU_HASH. A bpf_mem_alloc member
// identifies immediate node reuse; the older non-preallocated layout defers
// freeing until its RCU readers exit. Ignore same-named socket HASH types.
func ioControlHashAllocatorMember(spec *btf.Spec) (*btf.Member, error) {
	candidates, err := spec.AnyTypesByName("bpf_htab")
	if err != nil {
		return nil, fmt.Errorf("resolve struct bpf_htab in kernel BTF: %w", err)
	}
	found := false
	for _, candidate := range candidates {
		structure, ok := candidate.(*btf.Struct)
		if !ok {
			continue
		}
		var elems, extraElems, buckets bool
		for _, member := range structure.Members {
			switch member.Name {
			case "elems":
				elems = true
			case "extra_elems":
				extraElems = true
			case "n_buckets":
				buckets = true
			}
		}
		if !elems || !extraElems || !buckets {
			continue
		}
		found = true
		for _, member := range structure.Members {
			if btf.UnderlyingType(member.Type).TypeName() == "bpf_mem_alloc" {
				return &member, nil
			}
		}
	}
	if !found {
		return nil, fmt.Errorf("resolve ordinary HASH bpf_htab: %w", btf.ErrNotFound)
	}
	return nil, nil
}
