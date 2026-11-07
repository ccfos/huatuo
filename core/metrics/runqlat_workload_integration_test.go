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
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/pkg/metric"

	"github.com/cilium/ebpf"
)

func writeRunqlatMap(t testing.TB, object bpf.BPF, name string, key any, values ...latencyBpfData) {
	t.Helper()
	snapshot, err := ebpf.NewMapFromID(ebpf.MapID(object.MapIDByName(name)))
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	if err := snapshot.Update(key, values, ebpf.UpdateAny); err != nil {
		t.Fatal(err)
	}
}

func startRunqlatFixture(t testing.TB, kind string) (*runqlatCollector, bpf.BPF, *cpuWorkloadSource, string, *pod.Pod) {
	t.Helper()
	objectDir := os.Getenv("HUATUO_BPF_OBJECT_DIR")
	if objectDir == "" {
		t.Skip("set HUATUO_BPF_OBJECT_DIR for real runqlat/CSS probes")
	}
	previous := bpf.DefaultObjDir
	bpf.DefaultObjDir = objectDir
	t.Cleanup(func() { bpf.DefaultObjDir = previous })
	if err := bpf.Init(nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bpf.Shutdown)
	membership := fmt.Sprintf("/huatuo-runqlat-%d", os.Getpid())
	root := cgroups.RootFsFilePath("cpu")
	binding := pod.CgroupBinding{Controllers: map[string]string{"cpu": membership}}
	if cgroups.CgroupMode() == cgroups.Unified {
		root = cgroups.RootfsDefaultPath()
		binding = pod.CgroupBinding{Unified: membership}
	}
	directory := filepath.Join(root, membership)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(directory); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("remove runqlat cgroup: %v", err)
		}
	})
	workload := &pod.Pod{ID: "checkout-sandbox", Type: kind, Name: "checkout", StartedAt: time.Unix(100, 0), Cgroups: binding, Labels: map[string]any{pod.LabelHostNamespace: "production", pod.LabelWorkloadNamespace: "production"}}
	if kind == pod.TypeContainer {
		workload.Container = &pod.ContainerInfo{Type: pod.ContainerTypeNormal, Qos: pod.ContainerQosLevelMin, Hostname: "huatuo-dev", CgroupPath: membership}
	}
	source := &cpuWorkloadSource{workload: workload}
	if err := pod.StartManager(source, pod.ManagerOptions{SyncInterval: 50 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pod.ReleaseManager)
	var current *pod.Pod
	deadline := time.Now().Add(10 * time.Second)
	for {
		current, _ = pod.PodByID(workload.ID)
		if current != nil && current.Cgroups.CSS["cpu"] != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("CPU CSS did not bind to the test workload")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if current.Process != nil {
		t.Error("cgroup-only workload unexpectedly has a representative process")
	}
	object, err := bpf.LoadBPF("cpu_runqlat_tracing.o", nil)
	if err != nil {
		t.Fatal(err)
	}
	collector := &runqlatCollector{}
	if err := collector.bpf.Publish(object); err != nil {
		_ = object.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := collector.bpf.UnPublish(); err != nil {
			t.Errorf("close runqlat BPF: %v", err)
		}
	})
	css := current.Cgroups.CSS["cpu"]
	writeRunqlatMap(t, object, "cpu_tg_metric", css,
		latencyBpfData{NumLatencyZone0: 3, NumLatencyZone1: 5, NumLatencyZone2: 7, NumLatencyZone3: 9},
		latencyBpfData{NumLatencyZone0: 11, NumLatencyZone1: 13, NumLatencyZone2: 17, NumLatencyZone3: 19})
	writeRunqlatMap(t, object, "cpu_tg_metric", uint64(64), latencyBpfData{NumLatencyZone0: 999})
	writeRunqlatMap(t, object, "cpu_host_metric", uint32(0), latencyBpfData{NumLatencyZone0: 23, NumLatencyZone1: 29, NumLatencyZone2: 31, NumLatencyZone3: 37})
	return collector, object, source, directory, current
}

// TestRunqlatWorkloadIntegration 验证真实 cgroup CSS、BPF per-CPU map 和公共
// 输出的完整链路：容器及两类无 PID 沙箱、分区累加、未知 CSS 不归属、元数据刷新
// 保留缓存、真实调度事件增加计数，以及资源删除后只保留 Host 指标。
func TestRunqlatWorkloadIntegration(t *testing.T) {
	for _, kind := range []string{pod.TypeContainer, pod.TypeCubeSandbox, pod.TypeOpenSandbox} {
		t.Run(kind, func(t *testing.T) {
			collector, object, source, directory, workload := startRunqlatFixture(t, kind)
			data, err := collector.Update()
			if err != nil || len(data) != 8 {
				t.Errorf("runqlat metrics=%v err=%v", data, err)
				return
			}
			prefix := "workload_"
			if kind == pod.TypeContainer {
				prefix = "container_"
			}
			values := map[string]float64{"0": 14, "1": 18, "2": 24, "3": 28}
			host := map[string]float64{"0": 23, "1": 29, "2": 31, "3": 37}
			for _, item := range data {
				labels := item.Labels()
				expected := host[labels["zone"]]
				if item.Name() == prefix+"latency" {
					expected = values[labels["zone"]]
					if kind == pod.TypeContainer {
						if labels["container_host"] != "huatuo-dev" || labels["container_name"] != "checkout" || labels["container_type"] != "normal" || labels["container_hostnamespace"] != "production" || len(labels) != 8 {
							t.Errorf("container labels=%v", labels)
						}
					} else if labels["workload_id"] != workload.ID || labels["workload_type"] != kind || labels["workload_name"] != "checkout" || labels["workload_namespace"] != "production" || len(labels) != 7 {
						t.Errorf("sandbox labels=%v", labels)
					}
				} else if item.Name() != "latency" {
					t.Errorf("unexpected metric %s", item.Name())
				}
				if item.Type() != metric.MetricTypeCounter || item.Value != expected {
					t.Errorf("metric %s zone=%s type=%d value=%v want=%v", item.Name(), labels["zone"], item.Type(), item.Value, expected)
				}
			}
			baseline := workload.LifeResources("runqlat")
			source.mu.Lock()
			updated := *source.workload
			updated.Name = "checkout-updated"
			source.workload = &updated
			source.mu.Unlock()
			refreshed := waitCPUWorkload(t, "checkout-updated")
			if refreshed.LifeResources("runqlat") != baseline {
				t.Error("metadata refresh replaced runqlat cache")
			}
			owners := pod.PodsByCgroupCSS("cpu", pod.ContainerTypeNormal)
			for _, test := range []struct {
				name  string
				items []bpf.MapItem
				err   error
			}{
				{"dump failure", nil, errors.New("runqlat map unavailable")},
				{"short key", []bpf.MapItem{{Key: []byte{1}}}, nil},
				{"short value", []bpf.MapItem{{Key: binary.LittleEndian.AppendUint64(nil, workload.Cgroups.CSS["cpu"]), Value: []byte{1}}}, nil},
			} {
				t.Run(test.name, func(t *testing.T) {
					object := &ioControlWaitSnapshotBPF{mapName: "cpu_tg_metric", items: test.items, err: test.err}
					if err := collector.updateWorkloadDataCache(object, owners); err == nil {
						t.Error("invalid runqlat map was accepted")
					}
				})
			}
			if err := object.Attach(); err != nil {
				t.Fatal(err)
			}
			worker := exec.Command("sh", "-c", `echo "$$" > "$1/cgroup.procs"; while :; do :; done`, "runqlat-worker", directory)
			if err := worker.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if worker.ProcessState == nil {
					_ = worker.Process.Kill()
					_ = worker.Wait()
				}
			})
			deadline := time.Now().Add(10 * time.Second)
			for {
				data, err = collector.Update()
				if err != nil {
					t.Fatal(err)
				}
				var total float64
				for _, item := range data {
					if item.Name() == prefix+"latency" {
						total += item.Value
					}
				}
				if total > 84 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("live scheduler events did not update the workload counters")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := worker.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = worker.Wait()
			if err := os.Remove(directory); err != nil {
				t.Fatal(err)
			}
			deadline = time.Now().Add(10 * time.Second)
			for len(pod.PodsByCgroupCSS("cpu", pod.ContainerTypeNormal)) != 0 {
				if time.Now().After(deadline) {
					t.Fatal("removed CPU cgroup retained its CSS binding")
				}
				time.Sleep(10 * time.Millisecond)
			}
			data, err = collector.Update()
			if err != nil || len(data) != 4 {
				t.Errorf("removed workload metrics=%v err=%v", data, err)
			}
			for _, item := range data {
				if item.Name() != "latency" {
					t.Errorf("removed workload emitted %s", item.Name())
				}
			}
		})
	}
}

func BenchmarkRunqlatWorkload(b *testing.B) {
	collector, _, _, _, _ := startRunqlatFixture(b, pod.TypeContainer)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		data, err := collector.Update()
		if err != nil || len(data) != 8 {
			b.Fatalf("runqlat metrics=%v err=%v", data, err)
		}
	}
}
