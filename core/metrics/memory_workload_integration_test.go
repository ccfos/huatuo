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

//go:build integration

package collector

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/cgroups/paths"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/procfs"
)

const memoryMetricMembership = "/tenant/sandbox/8"

type memoryMetricSource struct {
	kind      string
	workloads map[string]*pod.Pod
}

func (s *memoryMetricSource) Type() string { return s.kind }
func (s *memoryMetricSource) Discover(context.Context) (pod.Discovery, error) {
	return pod.Discovery{Pods: s.workloads, Complete: true}, nil
}

func startMemoryMetricFixture(t testing.TB, kind string, legacy bool) string {
	t.Helper()
	root := t.TempDir()
	mount := filepath.Join(root, "memory-mount")
	directory := filepath.Join(mount, "sandbox/8")
	procRoot := filepath.Join(root, "proc")
	workload := &pod.Pod{
		ID: "checkout-memory", Type: kind, Name: "checkout", StartedAt: time.Unix(100, 0),
		Labels: map[string]any{pod.LabelHostNamespace: "checkout", pod.LabelWorkloadNamespace: "production"},
	}
	if kind == pod.TypeContainer {
		workload.Container = &pod.ContainerInfo{Type: pod.ContainerTypeNormal, Hostname: "huatuo-dev", CgroupPath: memoryMetricMembership}
	}
	if legacy {
		workload.Cgroups.Controllers = map[string]string{"memory": memoryMetricMembership}
		writeCPUFixture(t, filepath.Join(procRoot, "self/mountinfo"), fmt.Sprintf("40 1 0:40 /tenant %s rw - cgroup none rw,memory\n", mount))
	} else {
		workload.Cgroups.Unified = memoryMetricMembership
		writeCPUFixture(t, filepath.Join(procRoot, "self/mountinfo"), fmt.Sprintf("40 1 0:40 /tenant %s rw - cgroup2 none rw\n", mount))
	}
	writeCPUFixture(t, filepath.Join(directory, "memory.events"), "low 3\nhigh 5\noom 4\n")
	writeCPUFixture(t, filepath.Join(mount, "memory.events"), "low 999\noom 999\n")
	// Poison both legacy and unified host roots to catch accidental empty-path reads.
	for _, hostRoot := range []string{root, filepath.Join(root, "memory")} {
		writeCPUFixture(t, filepath.Join(hostRoot, "memory.stat"), "active_file 999\nrss 999\n")
		writeCPUFixture(t, filepath.Join(hostRoot, "memory.events"), "low 999\noom 999\n")
		writeCPUFixture(t, filepath.Join(hostRoot, "memory.local_direct_reclaim_time"), "999")
	}
	previousRoot := paths.RootfsDefaultPath
	paths.RootfsDefaultPath = root
	t.Cleanup(func() { paths.RootfsDefaultPath = previousRoot })
	previousBPFDir := bpf.DefaultObjDir
	bpf.DefaultObjDir = t.TempDir()
	t.Cleanup(func() { bpf.DefaultObjDir = previousBPFDir })
	source := &memoryMetricSource{kind: kind, workloads: map[string]*pod.Pod{workload.ID: workload}}
	if err := pod.StartManager(source, pod.ManagerOptions{ProcRoot: procRoot, SyncInterval: time.Hour}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pod.ReleaseManager)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		published, err := pod.PodByID(workload.ID)
		if err != nil {
			t.Fatal(err)
		}
		if published != nil {
			if published.Process != nil || len(published.Cgroups.CSS) != 0 {
				t.Error("memory fixture unexpectedly has a process or CSS")
			}
			return directory
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("memory-only workload was not published")
	return ""
}

// TestMemoryEventsWorkload 验证真实 Manager 发布的 memory-only 对象可读取事件
// 文件，覆盖 v1/v2、容器与沙箱标签、过滤、坏文件报错和缺失资源不回退宿主机。
func TestMemoryEventsWorkload(t *testing.T) {
	previous := configSnapshot()
	t.Cleanup(func() { Set(previous) })
	cfg := previous.Clone()
	cfg.MemoryEvents.Included = "^(low|high|oom)$"
	cfg.MemoryEvents.Excluded = "^high$"
	Set(cfg)
	for _, test := range []struct {
		name, kind string
		legacy     bool
	}{
		{"container v1", pod.TypeContainer, true},
		{"cube v1", pod.TypeCubeSandbox, true},
		{"cube v2", pod.TypeCubeSandbox, false},
		{"open v2", pod.TypeOpenSandbox, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := startMemoryMetricFixture(t, test.kind, test.legacy)
			collector := &memEventsCollector{}
			data, err := collector.Update()
			prefix := "workload_"
			if test.kind == pod.TypeContainer {
				prefix = "container_"
			}
			values := map[string]float64{}
			for _, item := range data {
				values[item.Name()] = item.Value
			}
			if err != nil || len(data) != 2 || values[prefix+"low"] != 3 || values[prefix+"oom"] != 4 {
				t.Errorf("memory events=%v err=%v", values, err)
			}
			writeCPUFixture(t, filepath.Join(directory, "memory.events"), "oom invalid\n")
			if _, err := collector.Update(); err == nil {
				t.Error("malformed events were ignored")
			}
			if err := os.Remove(filepath.Join(directory, "memory.events")); err != nil {
				t.Error(err)
				return
			}
			data, err = collector.Update()
			if err != nil || len(data) != 0 {
				t.Errorf("absent events used host data: count=%d err=%v", len(data), err)
			}
			if err := os.Remove(directory); err != nil {
				t.Error(err)
				return
			}
			data, err = collector.Update()
			if err != nil || len(data) != 0 {
				t.Errorf("absent memory binding used host data: count=%d err=%v", len(data), err)
			}
		})
	}
}

func BenchmarkMemoryEventsCollection(b *testing.B) {
	directory := startMemoryMetricFixture(b, pod.TypeContainer, true)
	root := b.TempDir()
	alias := filepath.Join(root, "memory", memoryMetricMembership)
	if err := os.MkdirAll(filepath.Dir(alias), 0o700); err != nil {
		b.Fatal(err)
	}
	if err := os.Symlink(directory, alias); err != nil {
		b.Fatal(err)
	}
	previousRoot := paths.RootfsDefaultPath
	paths.RootfsDefaultPath = root
	b.Cleanup(func() { paths.RootfsDefaultPath = previousRoot })
	previous := configSnapshot()
	Set(&Config{})
	b.Cleanup(func() { Set(previous) })
	attr, err := newMemEvents()
	if err != nil {
		b.Fatal(err)
	}
	collector := attr.TracingData.(*memEventsCollector)
	b.ReportAllocs()
	for b.Loop() {
		data, err := collector.Update()
		if err != nil || len(data) != 3 {
			b.Fatalf("events=%d err=%v", len(data), err)
		}
	}
}

// TestMemoryExtensionsWorkload 验证可选扩展通过公共 memory/unified 目录采集，
// 保持字段值和容器命名；缺少、损坏单个文件仅跳过该指标，目录消失不读取宿主机。
func TestMemoryExtensionsWorkload(t *testing.T) {
	for _, test := range []struct {
		name, kind string
		legacy     bool
	}{
		{"container", pod.TypeContainer, true},
		{"cube v1", pod.TypeCubeSandbox, true},
		{"cube v2", pod.TypeCubeSandbox, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := startMemoryMetricFixture(t, test.kind, test.legacy)
			writeCPUFixture(t, filepath.Join(directory, "memory.directstall_stat"), "directstall_time 12\n")
			writeCPUFixture(t, filepath.Join(directory, "memory.asynreclaim_stat"), "asyncreclaim_time 23\n")
			writeCPUFixture(t, filepath.Join(directory, "memory.local_direct_reclaim_time"), "34")
			collector := &memOthersCollector{}
			data, err := collector.Update()
			prefix := "workload_"
			if test.kind == pod.TypeContainer {
				prefix = "container_"
			}
			values := map[string]float64{}
			for _, item := range data {
				values[item.Name()] = item.Value
			}
			if err != nil || len(data) != 3 || values[prefix+"directstall_time"] != 12 || values[prefix+"asyncreclaim_time"] != 23 || values[prefix+"local_direct_reclaim_time"] != 34 {
				t.Errorf("extension values=%v err=%v", values, err)
			}
			if err := os.Remove(filepath.Join(directory, "memory.directstall_stat")); err != nil {
				t.Error(err)
				return
			}
			writeCPUFixture(t, filepath.Join(directory, "memory.asynreclaim_stat"), "asyncreclaim_time invalid\n")
			data, err = collector.Update()
			if err != nil || len(data) != 1 || data[0].Name() != prefix+"local_direct_reclaim_time" {
				t.Errorf("unavailable extension blocked healthy data: %v %v", data, err)
			}
			if err := os.RemoveAll(directory); err != nil {
				t.Error(err)
				return
			}
			data, err = collector.Update()
			if err != nil || len(data) != 0 {
				t.Errorf("missing binding read host extensions: %d %v", len(data), err)
			}
		})
	}
}

func BenchmarkMemoryExtensionsCollection(b *testing.B) {
	directory := startMemoryMetricFixture(b, pod.TypeContainer, true)
	writeCPUFixture(b, filepath.Join(directory, "memory.directstall_stat"), "directstall_time 12\n")
	writeCPUFixture(b, filepath.Join(directory, "memory.asynreclaim_stat"), "asyncreclaim_time 23\n")
	writeCPUFixture(b, filepath.Join(directory, "memory.local_direct_reclaim_time"), "34")
	root := b.TempDir()
	alias := filepath.Join(root, "memory", memoryMetricMembership)
	if err := os.MkdirAll(filepath.Dir(alias), 0o700); err != nil {
		b.Fatal(err)
	}
	if err := os.Symlink(directory, alias); err != nil {
		b.Fatal(err)
	}
	previous := paths.RootfsDefaultPath
	paths.RootfsDefaultPath = root
	b.Cleanup(func() { paths.RootfsDefaultPath = previous })
	collector := &memOthersCollector{}
	b.ReportAllocs()
	for b.Loop() {
		data, err := collector.Update()
		if err != nil || len(data) != 3 {
			b.Fatalf("extensions=%d err=%v", len(data), err)
		}
	}
}

// TestMemoryVmstatWorkload 验证容器和沙箱复用旧字段过滤，Host 过滤仍独立；
// 覆盖 v1/v2 memory-only 对象、运行时过滤更新、坏文件及资源消失、Host 读取失败。
func TestMemoryVmstatWorkload(t *testing.T) {
	previous := configSnapshot()
	t.Cleanup(func() { Set(previous) })
	cfg := previous.Clone()
	cfg.Vmstat.IncludedOnContainer = "^(active_file|total_active_file|rss)$"
	cfg.Vmstat.ExcludedOnContainer = "^total_"
	cfg.Vmstat.IncludedOnHost = "^(nr_free_pages|pgfault)$"
	cfg.Vmstat.ExcludedOnHost = "^pgfault$"
	for _, test := range []struct {
		name, kind string
		legacy     bool
	}{
		{"container v1", pod.TypeContainer, true},
		{"cube v1", pod.TypeCubeSandbox, true},
		{"cube v2", pod.TypeCubeSandbox, false},
		{"open v2", pod.TypeOpenSandbox, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			Set(cfg)
			directory := startMemoryMetricFixture(t, test.kind, test.legacy)
			file := filepath.Join(directory, "memory.stat")
			writeCPUFixture(t, file, "active_file 4096\ntotal_active_file 8192\nrss 16384\npgfault 6\n")
			originalPrefix := filepath.Dir(procfs.DefaultPath())
			procRoot := t.TempDir()
			procfs.RootPrefix(procRoot)
			t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })
			hostFile := procfs.Path("vmstat")
			writeCPUFixture(t, hostFile, "nr_free_pages 7\npgfault 9\n")
			prefix := "workload_"
			if test.kind == pod.TypeContainer {
				prefix = "container_"
			}
			collector := &memoryVmStat{}
			data, err := collector.Update()
			values := map[string]float64{}
			for _, item := range data {
				values[item.Name()] = item.Value
			}
			if err != nil || len(data) != 3 || values[prefix+"active_file"] != 4096 || values[prefix+"rss"] != 16384 || values["nr_free_pages"] != 7 {
				t.Errorf("vmstat values=%v err=%v", values, err)
			}
			updated := cfg.Clone()
			updated.Vmstat.IncludedOnContainer = "^rss$"
			Set(updated)
			data, err = collector.Update()
			values = map[string]float64{}
			for _, item := range data {
				values[item.Name()] = item.Value
			}
			if err != nil || len(data) != 2 || values[prefix+"rss"] != 16384 || values["nr_free_pages"] != 7 {
				t.Errorf("live filter update=%v err=%v", values, err)
			}
			updated.Vmstat.IncludedOnContainer = "["
			Set(updated)
			if _, err := collector.Update(); err == nil {
				t.Error("invalid legacy filter accepted")
			}
			Set(cfg)
			if err := os.Remove(hostFile); err != nil {
				t.Error(err)
				return
			}
			data, err = collector.Update()
			if err != nil || len(data) != 2 {
				t.Errorf("Host failure discarded cgroup metrics: count=%d err=%v", len(data), err)
			}
			writeCPUFixture(t, hostFile, "nr_free_pages 7\npgfault 9\n")
			writeCPUFixture(t, file, "rss invalid\n")
			data, err = collector.Update()
			if err != nil || len(data) != 1 || data[0].Name() != "nr_free_pages" {
				t.Errorf("bad cgroup file discarded host metrics: %v %v", data, err)
			}
			if err := os.Remove(file); err != nil {
				t.Error(err)
				return
			}
			data, err = collector.Update()
			if err != nil || len(data) != 1 || data[0].Name() != "nr_free_pages" {
				t.Errorf("absent file used root cgroup data: %v %v", data, err)
			}
			if err := os.RemoveAll(directory); err != nil {
				t.Error(err)
				return
			}
			data, err = collector.Update()
			if err != nil || len(data) != 1 || data[0].Name() != "nr_free_pages" {
				t.Errorf("absent binding used root cgroup data: %v %v", data, err)
			}
		})
	}
}

func BenchmarkMemoryVmstatCollection(b *testing.B) {
	directory := startMemoryMetricFixture(b, pod.TypeContainer, true)
	writeCPUFixture(b, filepath.Join(directory, "memory.stat"), "active_file 4096\nrss 16384\npgfault 6\n")
	root := b.TempDir()
	alias := filepath.Join(root, "memory", memoryMetricMembership)
	if err := os.MkdirAll(filepath.Dir(alias), 0o700); err != nil {
		b.Fatal(err)
	}
	if err := os.Symlink(directory, alias); err != nil {
		b.Fatal(err)
	}
	previousRoot := paths.RootfsDefaultPath
	paths.RootfsDefaultPath = root
	b.Cleanup(func() { paths.RootfsDefaultPath = previousRoot })
	previous := configSnapshot()
	Set(&Config{})
	b.Cleanup(func() { Set(previous) })
	attr, err := newMemoryVmStat()
	if err != nil {
		b.Fatal(err)
	}
	collector := attr.TracingData.(*memoryVmStat)
	b.ReportAllocs()
	for b.Loop() {
		data, err := collector.workloadVmstat()
		if err != nil || len(data) != 3 {
			b.Fatalf("vmstat=%d err=%v", len(data), err)
		}
	}
}
