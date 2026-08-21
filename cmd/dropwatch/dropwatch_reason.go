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

package main

import (
	"fmt"

	"github.com/cilium/ebpf/btf"
)

// Must match SKB_DROP_REASON_NOT_SUPPORTED in bpf/dropwatch.c.
const skbDropReasonNotSupported int32 = -1

type dropReason map[uint32]string

// NewDropReason loads kernel drop-reason names when BTF exposes them.
func NewDropReason() (dropReason, error) {
	spec, err := btf.LoadKernelSpec()
	if err != nil {
		return nil, fmt.Errorf("load kernel BTF: %w", err)
	}

	var enum *btf.Enum
	if err := spec.TypeByName("skb_drop_reason", &enum); err != nil {
		return nil, fmt.Errorf("load skb_drop_reason: %w", err)
	}

	names := make(dropReason, len(enum.Values))
	for _, v := range enum.Values {
		names[uint32(v.Value)] = v.Name
	}
	return names, nil
}

// Resolve returns the kernel name for a drop reason or its numeric value.
func (r dropReason) Resolve(v uint32) string {
	if int32(v) == skbDropReasonNotSupported {
		return "NOT_SUPPORTED"
	}
	if r != nil {
		if name, ok := r[v]; ok {
			return name
		}
	}
	return fmt.Sprintf("%d", v)
}
