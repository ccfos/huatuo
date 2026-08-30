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

// This file shares IO operation labels and optional container attribution.
// Each collector owns its BPF session and cumulative counter baseline.

package collector

import (
	"errors"
	"fmt"
	"github.com/cilium/ebpf/btf"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
)

const ioControlHostNamespaceKey = "HostNamespace"

type ioControlContainerSource func() (map[string]*pod.Container, error)

type ioControlContainerLabels struct {
	hostname      string
	name          string
	containerType string
	qos           string
	hostNamespace string
}

// A failed catalog is unavailable, not empty: capture may continue without
// attribution, but registration refresh must retain its existing mappings.
func ioControlQueryContainers(source ioControlContainerSource) (map[string]*pod.Container, bool) {
	containers, err := source()
	if err != nil {
		log.Warnf("IO container attribution unavailable: %v", err)
		return nil, false
	}
	return containers, true
}

// Missing CSS associations and invalid public labels only suppress container
// attribution. They cannot invalidate host counters or hold back the baseline.
func ioControlContainerAttribution(
	containers map[uint64]*pod.Container,
	css uint64,
) (*pod.Container, ioControlContainerLabels) {
	container := containers[css]
	if css == 0 || container == nil {
		return nil, ioControlContainerLabels{}
	}
	labels, err := ioControlPublicContainerLabels(container)
	if err != nil {
		log.Warnf("IO container attribution unavailable: %v", err)
		return nil, ioControlContainerLabels{}
	}
	return container, labels
}

func ioControlPublicContainerLabels(
	container *pod.Container,
) (ioControlContainerLabels, error) {
	if container == nil {
		return ioControlContainerLabels{}, errors.New("nil iocontrol container")
	}
	hostNamespace, ok := container.Labels[ioControlHostNamespaceKey].(string)
	if !ok {
		return ioControlContainerLabels{}, fmt.Errorf(
			"container %q has no string %s label",
			container.ID,
			ioControlHostNamespaceKey,
		)
	}
	return ioControlContainerLabels{
		hostname:      container.Hostname,
		name:          container.Name,
		containerType: container.Type.String(),
		qos:           container.Qos.String(),
		hostNamespace: hostNamespace,
	}, nil
}

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
