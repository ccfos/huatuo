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

package events

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/cgroups/paths"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/pkg/metric"

	"github.com/cilium/ebpf"
)

func hungTaskTestContainers() map[string]*pod.Container {
	return map[string]*pod.Container{
		"test": {ID: "test", Name: "workload", CgroupPath: "/kubepods/test", Labels: map[string]any{"HostNamespace": "test"}},
	}
}

func hungTaskEvent(ids ...uint64) abi.HungtaskEvent {
	event := abi.HungtaskEvent{TID: 321, CgroupCount: uint32(len(ids))}
	copy(event.CgroupIds[:], ids)
	return event
}

func TestHungTaskRecordIdentity(t *testing.T) {
	original := atomic.LoadInt64(&hungtaskCounter)
	t.Cleanup(func() { atomic.StoreInt64(&hungtaskCounter, original) })
	c := hungTaskTracing{}
	discover := func() (map[string]*pod.Container, error) { return hungTaskTestContainers(), nil }
	event := hungTaskEvent(100, 1<<32|42)
	container, err := c.record(&event, discover, func(string) (uint64, error) { return 1<<32 | 42, nil })
	if err != nil || container == nil || container.ID != "test" {
		t.Fatalf("event cgroup attribution: container=%v err=%v", container, err)
	}
	data, err := c.update(discover)
	if err != nil {
		t.Fatal(err)
	}
	assertHungTaskMetrics(t, data, map[string]float64{"total": float64(original + 1), "container_total": 1})
}

func assertHungTaskMetrics(t *testing.T, data []*metric.Data, want map[string]float64) {
	t.Helper()
	if len(data) != len(want) {
		t.Fatalf("got %d metrics, want %d", len(data), len(want))
	}
	for _, m := range data {
		value, ok := want[m.Name()]
		if !ok || m.Value != value || m.Type() != metric.MetricTypeCounter {
			t.Fatalf("unexpected metric %s=%v", m.Name(), m.Value)
		}
		if m.Name() == "container_total" && m.Labels()["container_name"] != "workload" {
			t.Fatalf("labels: %v", m.Labels())
		}
		delete(want, m.Name())
	}
}

func TestHungTaskBPFLayout(t *testing.T) {
	object := os.Getenv("HUATUO_HUNGTASK_BPF_OBJECT")
	if object == "" {
		t.Skip("set HUATUO_HUNGTASK_BPF_OBJECT to the compiled object")
	}
	spec, err := ebpf.LoadCollectionSpec(object)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Programs) != 2 || spec.Programs["raw_sched_process_hang"].Type != ebpf.RawTracepoint ||
		spec.Programs["tracepoint_sched_process_hang"].Type != ebpf.TracePoint || abi.HungtaskEventSize != 152 {
		t.Fatal("unexpected hungtask programs or event ABI")
	}
	for _, unified := range []uint32{0, 1} {
		if err := spec.Copy().RewriteConstants(map[string]any{"unified_cgroups": unified}); err != nil {
			t.Fatal(err)
		}
	}
	if os.Getenv("HUATUO_BPF_INTEGRATION") != "1" {
		return
	}
	previous := bpf.DefaultObjDir
	bpf.DefaultObjDir = filepath.Dir(object)
	t.Cleanup(func() { bpf.DefaultObjDir = previous })
	// Only attach: do not induce hung tasks or alter the system timeout.
	for _, containers := range []bool{false, true} {
		obj, err := loadHungTaskObject(filepath.Base(object), containers)
		if err != nil {
			t.Fatal(err)
		}
		func() {
			defer obj.Close()
			reader, err := obj.AttachAndEventPipe(context.Background(), "hungtask_perf_events", 8192)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			if (obj.ProgramIDByName("raw_sched_process_hang") != 0) != containers {
				t.Fatal("wrong capture mode")
			}
		}()
	}
}

func BenchmarkHungTaskContainerKernfs(b *testing.B) {
	root := os.Getenv("HUATUO_HUNGTASK_CGROUP_PATH")
	if root == "" {
		b.Skip("set HUATUO_HUNGTASK_CGROUP_PATH to a cgroup directory")
	}
	id, err := paths.KernfsID(root)
	if err != nil {
		b.Fatal(err)
	}
	containers := hungTaskTestContainers()
	event := hungTaskEvent(id)
	resolve := func(string) (uint64, error) { return paths.KernfsID(root) }
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if hungTaskContainer(&event, containers, resolve) == nil {
			b.Fatal("missing container")
		}
	}
}
