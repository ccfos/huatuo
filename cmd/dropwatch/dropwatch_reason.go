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

	"huatuo-bamai/internal/log"
)

// Must match SKB_DROP_REASON_NOT_SUPPORTED in bpf/dropwatch.c.
const skbDropReasonNotSupported int32 = -1

type dropReasonNames map[uint32]string

func loadDropReasonNames() dropReasonNames {
	spec, err := btf.LoadKernelSpec()
	if err != nil {
		log.Debugf("dropwatch: load kernel BTF for drop reason names: %v", err)
		return nil
	}

	var enum btf.Enum
	if err := spec.TypeByName("skb_drop_reason", &enum); err != nil {
		log.Debugf("dropwatch: skb_drop_reason enum not found in kernel BTF (kernel < 5.17?): %v", err)
		return nil
	}

	names := make(dropReasonNames, len(enum.Values))
	for _, v := range enum.Values {
		names[uint32(v.Value)] = v.Name
	}
	return names
}

func (m dropReasonNames) resolve(v uint32) string {
	if int32(v) == skbDropReasonNotSupported {
		return "NOT_SUPPORTED"
	}
	if m != nil {
		if name, ok := m[v]; ok {
			return name
		}
	}
	return fmt.Sprintf("%d", v)
}
