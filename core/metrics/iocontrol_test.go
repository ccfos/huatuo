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

// This file supplies container-source and kernel-BTF fixtures for IO tests.

package collector

import (
	"time"

	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	"github.com/ccfos/huatuo/internal/pod"

	"github.com/cilium/ebpf/btf"
)

func ioControlHashBTFFixture(extra ...btf.Member) *btf.Struct {
	word := &btf.Int{Name: "unsigned long", Size: 8}
	hash := &btf.Struct{Name: "bpf_htab", Size: 24, Members: []btf.Member{
		{Name: "elems", Type: word},
		{Name: "extra_elems", Type: word, Offset: 64},
		{Name: "n_buckets", Type: word, Offset: 128},
	}}
	for _, member := range extra {
		member.Offset = btf.Bits(hash.Size * 8)
		hash.Members = append(hash.Members, member)
		hash.Size += 8
	}
	return hash
}

func newIOControlAttributionTestContainer(
	id string,
	css uint64,
	startedAt time.Time,
) *pod.Container {
	return &pod.Container{
		ID:        id,
		Name:      "shared-name",
		Hostname:  "shared-host",
		Type:      pod.ContainerTypeNormal,
		Qos:       pod.ContainerQosLevelMin,
		StartedAt: startedAt,
		CgroupCss: map[string]uint64{
			subsystem.SubsystemBlkIO: css,
		},
		Labels: map[string]any{
			ioControlHostNamespaceKey: "shared-namespace",
		},
	}
}

type ioControlAttributionTestSource struct {
	containers map[string]*pod.Container
	err        error
}

func (source *ioControlAttributionTestSource) read() (
	map[string]*pod.Container,
	error,
) {
	return source.containers, source.err
}
