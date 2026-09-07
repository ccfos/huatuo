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

package collector

import (
	"encoding/binary"
	"os"
	"testing"

	"github.com/cilium/ebpf"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/pod"
)

func TestMemcgReclaimBPFLayout(t *testing.T) {
	object := os.Getenv("HUATUO_MEMCG_RECLAIM_BPF_OBJECT")
	if object == "" {
		t.Skip("set HUATUO_MEMCG_RECLAIM_BPF_OBJECT to a freshly built object")
	}
	spec, err := ebpf.LoadCollectionSpec(object)
	if err != nil {
		t.Fatal(err)
	}
	m := spec.Maps["memory_cgroup_allocpages_stall"]
	if m == nil || m.Type != ebpf.Hash || m.KeySize != 8 ||
		m.ValueSize != uint32(binary.Size(memoryBpfStruct{})) || m.MaxEntries != 10240 {
		t.Fatalf("unexpected count map: %+v", m)
	}
	if spec.Maps["memory_cgroup_reclaim_start"] != nil {
		t.Fatal("count-only collection must not retain a timing map")
	}
	host := spec.Maps["memory_host_directstall"]
	if host == nil || host.Type != ebpf.PerCPUArray || host.KeySize != 4 || host.ValueSize != 8 || host.MaxEntries != 1 {
		t.Fatalf("unexpected host count map: %+v", host)
	}
	for _, name := range []string{
		"tracepoint_vmscan_mm_vmscan_memcg_reclaim_begin",
		"kprobe_mem_cgroup_css_released",
	} {
		if spec.Programs[name] == nil {
			t.Fatalf("missing BPF program %s", name)
		}
	}
	if len(spec.Programs) != 2 {
		t.Fatalf("got %d BPF programs, want 2", len(spec.Programs))
	}
	if os.Getenv("HUATUO_BPF_INTEGRATION") != "1" {
		return
	}
	raw, err := os.ReadFile(object)
	if err != nil {
		t.Fatal(err)
	}
	obj, err := bpf.LoadBPFFromBytes("memory_reclaim", raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer obj.Close()
	if err := obj.Attach(); err != nil {
		t.Fatal(err)
	}
	c := memoryCgroupReclaim{}
	data, err := c.update(obj, func() (map[string]*pod.Container, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 1 || data[0].Name() != "directstall" {
		t.Fatalf("expected one host directstall metric, got %v", data)
	}
}
