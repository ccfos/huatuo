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
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/cgroups/paths"
	v1 "github.com/ccfos/huatuo/internal/cgroups/v1"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/pkg/metric"
)

type cpuWorkloadSource struct {
	mu       sync.Mutex
	workload *pod.Pod
}

func (s *cpuWorkloadSource) Type() string { return s.workload.Type }
func (s *cpuWorkloadSource) Discover(context.Context) (pod.Discovery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return pod.Discovery{Pods: map[string]*pod.Pod{s.workload.ID: s.workload}, Complete: true}, nil
}

func writeCPUFixture(t testing.TB, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitCPUWorkload(t testing.TB, name string) *pod.Pod {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		workload, err := pod.PodByID("checkout-sandbox")
		if err != nil {
			t.Fatal(err)
		}
		if workload != nil && workload.Name == name {
			return workload
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("CPU fixture was not published by the Manager")
	return nil
}

func startCPUFixture(t testing.TB, kind string, legacy bool, interval time.Duration) (*cpuWorkloadSource, map[string]string) {
	t.Helper()
	root := t.TempDir()
	procRoot := filepath.Join(root, "proc")
	workload := &pod.Pod{
		ID: "checkout-sandbox", Type: kind, StartedAt: time.Unix(100, 0),
		Labels: map[string]any{pod.LabelHostNamespace: "checkout", pod.LabelWorkloadNamespace: "production", "private": true},
	}
	if kind == pod.TypeContainer {
		workload.Container = &pod.ContainerInfo{Type: pod.ContainerTypeNormal, Hostname: "huatuo-dev", CgroupPath: "/tenant/cpu/8"}
	}
	dirs := make(map[string]string)
	var mounts strings.Builder
	if legacy {
		workload.Cgroups.Controllers = make(map[string]string)
		for index, controller := range []string{"cpu", "cpuacct", "cpuset"} {
			mount := filepath.Join(root, controller+"-mount")
			membership := "/tenant/" + controller + "/8"
			dirs[controller] = filepath.Join(mount, controller, "8")
			workload.Cgroups.Controllers[controller] = membership
			fmt.Fprintf(&mounts, "%d 1 0:%d /tenant %s rw - cgroup none rw,%s\n", 40+index, 40+index, mount, controller)
		}
		writeCPUFixture(t, filepath.Join(dirs["cpu"], "cpu.cfs_quota_us"), "-1")
		writeCPUFixture(t, filepath.Join(dirs["cpu"], "cpu.cfs_period_us"), "100000")
		writeCPUFixture(t, filepath.Join(dirs["cpu"], "cpu.stat"), "nr_throttled 3\nthrottled_time 5000\nwait_sum 9000\n")
		writeCPUFixture(t, filepath.Join(dirs["cpuacct"], "cpuacct.usage"), "2000000000")
		writeCPUFixture(t, filepath.Join(dirs["cpuacct"], "cpuacct.stat"), "user 100\nsystem 50\n")
		writeCPUFixture(t, filepath.Join(dirs["cpuset"], "cpuset.cpus"), "1-2")
	} else {
		mount := filepath.Join(root, "unified-mount")
		workload.Cgroups.Unified = "/tenant/sandbox/8"
		dirs["cpu"] = filepath.Join(mount, "sandbox/8")
		fmt.Fprintf(&mounts, "40 1 0:40 /tenant %s rw - cgroup2 none rw\n", mount)
		writeCPUFixture(t, filepath.Join(dirs["cpu"], "cpu.max"), "max 100000")
		writeCPUFixture(t, filepath.Join(dirs["cpu"], "cpuset.cpus.effective"), "1-2")
		writeCPUFixture(t, filepath.Join(dirs["cpu"], "cpu.stat"), "usage_usec 2000000\nuser_usec 1500000\nsystem_usec 500000\nnr_throttled 3\nthrottled_usec 5\n")
	}
	writeCPUFixture(t, filepath.Join(procRoot, "self/mountinfo"), mounts.String())
	previousBPFDir := bpf.DefaultObjDir
	bpf.DefaultObjDir = t.TempDir()
	t.Cleanup(func() { bpf.DefaultObjDir = previousBPFDir })
	source := &cpuWorkloadSource{workload: workload}
	if err := pod.StartManager(source, pod.ManagerOptions{ProcRoot: procRoot, SyncInterval: interval}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pod.ReleaseManager)
	waitCPUWorkload(t, "")
	return source, dirs
}

// TestCPUWorkloadCollection 验证 CPU 采集经 Source/Manager 读取真实临时文件，覆盖
// v1 分控制器且挂载子树、v2、旧容器及两类无 PID 沙箱、差值和 cpuset 容量、元数据刷新
// 保留基线，以及 cpuacct/cpu 目录消失不会退回宿主机资源。
func TestCPUWorkloadCollection(t *testing.T) {
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
			source, dirs := startCPUFixture(t, test.kind, test.legacy, 10*time.Millisecond)
			workload := waitCPUWorkload(t, "")
			if workload.Process != nil || len(workload.Cgroups.CSS) != 0 {
				t.Error("fixture unexpectedly requires process or CSS")
			}
			util := &cpuUtilCollector{cgroup: &cpuUsageCgroup{}, numCores: 4}
			stat := &cpuStatCollector{}
			prefix := "workload_"
			if test.kind == pod.TypeContainer {
				prefix = "container_"
			}
			metrics, err := util.Update()
			if err != nil || len(metrics) != 7 {
				t.Errorf("initial CPU metrics=%d err=%v", len(metrics), err)
				return
			}
			cache := workload.LifeResources("collector_cpu_util").(*cpuUtilStat)
			cache.lastTimestamp = time.Now().Add(-2 * time.Second)
			previous := cache.lastTimestamp
			if test.legacy {
				writeCPUFixture(t, filepath.Join(dirs["cpuacct"], "cpuacct.usage"), "3000000000")
			} else {
				writeCPUFixture(t, filepath.Join(dirs["cpu"], "cpu.stat"), "usage_usec 3000000\nuser_usec 2500000\nsystem_usec 500000\nnr_throttled 3\nthrottled_usec 5\n")
			}
			metrics, err = util.Update()
			if err != nil {
				t.Error(err)
				return
			}
			values := make(map[string]float64)
			for _, data := range metrics {
				values[data.Name()] = data.Value
			}
			want := 1000000 * 100 / (2 * float64(cache.lastTimestamp.Sub(previous).Microseconds()))
			if values[prefix+"cores"] != 2 || math.Abs(values[prefix+"total"]-want) > 1e-9 {
				t.Errorf("CPU values=%v, want cores=2 total=%v", values, want)
			}
			metrics, err = stat.Update()
			values = make(map[string]float64)
			for _, data := range metrics {
				values[data.Name()] = data.Value
			}
			if err != nil || values[prefix+"nr_throttled"] != 3 || values[prefix+"throttled_time"] != 5000 {
				t.Errorf("stat values=%v err=%v", values, err)
			}
			if test.legacy {
				writeCPUFixture(t, filepath.Join(dirs["cpu"], "cpu.stat"), "nr_throttled 3\nthrottled_time 5000\nwait_sum 1009000\n")
				writeCPUFixture(t, filepath.Join(dirs["cpuacct"], "cpuacct.usage"), "3001000000")
				workload.LifeResources("collector_cpu_stat").(*cpuStat).lastUpdate = time.Now().Add(-2 * time.Second)
				metrics, err = stat.Update()
				found := false
				for _, data := range metrics {
					if data.Name() == prefix+"wait_sum_percent" {
						found = true
						if data.Value != 50 {
							t.Errorf("wait percent=%v, want 50", data.Value)
						}
					}
				}
				if err != nil || !found {
					t.Errorf("wait delta missing: %v", err)
				}
			}
			source.mu.Lock()
			updated := *source.workload
			updated.Name = "checkout-renamed"
			source.workload = &updated
			source.mu.Unlock()
			refreshed := waitCPUWorkload(t, "checkout-renamed")
			if refreshed.LifeResources("collector_cpu_util") != cache || cache.lastUsage.Usage != 3000000 {
				t.Error("metadata refresh reset CPU baseline")
			}
			metrics, err = util.Update()
			if err != nil {
				t.Error(err)
				return
			}
			for _, data := range metrics {
				if data.Name() != prefix+"total" {
					continue
				}
				labelName := metric.LabelWorkloadName
				if test.kind == pod.TypeContainer {
					labelName = metric.LabelContainerName
				}
				if data.Labels()[labelName] != "checkout-renamed" {
					t.Errorf("stale name labels: %v", data.Labels())
				}
				if _, exposed := data.Labels()["private"]; exposed {
					t.Error("source labels leaked into CPU metrics")
				}
			}
			if test.legacy {
				if err := os.RemoveAll(dirs["cpuacct"]); err != nil {
					t.Error(err)
					return
				}
				cache.lastTimestamp = time.Now().Add(-2 * time.Second)
				metrics, err = util.Update()
				if err != nil || len(metrics) != 3 {
					t.Errorf("missing cpuacct used a host fallback: metrics=%d err=%v", len(metrics), err)
				}
				refreshed.LifeResources("collector_cpu_stat").(*cpuStat).lastUpdate = time.Time{}
				metrics, err = stat.Update()
				if err != nil || len(metrics) != 2 {
					t.Errorf("missing cpuacct disabled throttling: metrics=%d err=%v", len(metrics), err)
				}
			}
			if err := os.RemoveAll(dirs["cpu"]); err != nil {
				t.Error(err)
				return
			}
			metrics, err = util.Update()
			if err != nil || len(metrics) != 3 {
				t.Errorf("missing cpu used a host fallback: metrics=%d err=%v", len(metrics), err)
			}
		})
	}
}

func BenchmarkCPUWorkloadCollection(b *testing.B) {
	for _, legacy := range []bool{true, false} {
		b.Run(fmt.Sprintf("legacy=%t", legacy), func(b *testing.B) {
			startCPUFixture(b, pod.TypeCubeSandbox, legacy, time.Hour)
			collector := &cpuUtilCollector{cgroup: &cpuUsageCgroup{}, numCores: 4}
			workload := waitCPUWorkload(b, "")
			cache := workload.LifeResources("collector_cpu_util").(*cpuUtilStat)
			b.ReportAllocs()
			for b.Loop() {
				cache.lastTimestamp = time.Now().Add(-2 * time.Second)
				var data []*metric.Data
				var err error
				data, err = collector.Update()
				if err != nil || len(data) != 7 {
					b.Fatalf("CPU collection metrics=%d err=%v", len(data), err)
				}
			}
		})
	}
}

// BenchmarkContainerCPUCollection compares the old and new Update loops on the
// same files; legacy paths are aliases of the Manager's resolved directories.
func BenchmarkContainerCPUCollection(b *testing.B) {
	_, dirs := startCPUFixture(b, pod.TypeContainer, true, time.Hour)
	root := b.TempDir()
	for _, controller := range []string{"cpu", "cpuacct", "cpuset"} {
		alias := filepath.Join(root, controller, "tenant/cpu/8")
		if err := os.MkdirAll(filepath.Dir(alias), 0o700); err != nil {
			b.Fatal(err)
		}
		if err := os.Symlink(dirs[controller], alias); err != nil {
			b.Fatal(err)
		}
	}
	writeCPUFixture(b, filepath.Join(root, "cpuacct/cpuacct.stat"), "user 0\nsystem 0\n")
	writeCPUFixture(b, filepath.Join(root, "cpuacct/cpuacct.usage"), "0")
	previous := paths.RootfsDefaultPath
	paths.RootfsDefaultPath = root
	b.Cleanup(func() { paths.RootfsDefaultPath = previous })
	backend, err := v1.New()
	if err != nil {
		b.Fatal(err)
	}
	collector := &cpuUtilCollector{cgroup: backend, numCores: 4}
	workload := waitCPUWorkload(b, "")
	cache := workload.LifeResources("collector_cpu_util").(*cpuUtilStat)
	b.ReportAllocs()
	for b.Loop() {
		cache.lastTimestamp = time.Now().Add(-2 * time.Second)
		collector.cpuDataCache.lastTimestamp = cache.lastTimestamp
		data, err := collector.Update()
		if err != nil || len(data) != 7 {
			b.Fatalf("CPU metrics=%d err=%v", len(data), err)
		}
	}
}
