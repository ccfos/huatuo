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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/paths"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/pkg/metric"
)

func startLoadavgFixture(t testing.TB, kind string, containerType pod.ContainerType, kata bool) string {
	t.Helper()
	if cgroups.CgroupMode() != cgroups.Legacy {
		t.Skip("TASKSTATS workload load requires legacy cgroups")
	}
	membership := fmt.Sprintf("/huatuo-loadavg-%d", os.Getpid())
	directory := paths.Path("cpu", membership)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(directory); err != nil && !os.IsNotExist(err) {
			t.Errorf("remove test cgroup: %v", err)
		}
	})
	workload := &pod.Pod{
		ID: "checkout-sandbox", Type: kind, Name: "checkout", StartedAt: time.Unix(100, 0),
		Labels:  map[string]any{pod.LabelHostNamespace: "production", pod.LabelWorkloadNamespace: "production"},
		Cgroups: pod.CgroupBinding{Controllers: map[string]string{"cpu": membership}},
	}
	if kind == pod.TypeContainer {
		workload.Container = &pod.ContainerInfo{Type: containerType, Qos: pod.ContainerQosLevelMin, Hostname: "huatuo-dev", CgroupPath: membership}
		if kata {
			workload.Container.RuntimeType = "io.containerd.kata.v2"
			workload.Container.SandboxID = "checkout-vm"
		}
	}
	previous := bpf.DefaultObjDir
	bpf.DefaultObjDir = t.TempDir()
	t.Cleanup(func() { bpf.DefaultObjDir = previous })
	if err := pod.StartManager(&cpuWorkloadSource{workload: workload}, pod.ManagerOptions{SyncInterval: time.Hour}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pod.ReleaseManager)
	current := waitCPUWorkload(t, "checkout")
	if current.Process != nil || len(current.Cgroups.CSS) != 0 {
		t.Error("loadavg fixture unexpectedly requires process or CSS")
	}
	return directory
}

// TestLoadavgWorkloadIntegration 验证真实 v1 CPU cgroup 与 TASKSTATS 的采集链路：
// 普通容器、sidecar、Kata 及两类无 PID 沙箱输出非零可运行任务数，保留容器标签；
// DaemonSet 容器排除，任务退出后归零，目录删除后跳过且宿主机负载仍可采集。
func TestLoadavgWorkloadIntegration(t *testing.T) {
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, kind     string
		containerType  pod.ContainerType
		kata, excluded bool
	}{
		{"normal", pod.TypeContainer, pod.ContainerTypeNormal, false, false},
		{"sidecar", pod.TypeContainer, pod.ContainerTypeSidecar, false, false},
		{"kata", pod.TypeContainer, pod.ContainerTypeNormal, true, false},
		{"cube", pod.TypeCubeSandbox, 0, false, false},
		{"open", pod.TypeOpenSandbox, 0, false, false},
		{"daemonset", pod.TypeContainer, pod.ContainerTypeDaemonSet, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := startLoadavgFixture(t, test.kind, test.containerType, test.kata)
			worker := exec.Command("sh", "-c", `echo "$$" > "$1/cgroup.procs"; while :; do :; done`, "loadavg-worker", directory)
			if err := worker.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if worker.ProcessState == nil {
					_ = worker.Process.Kill()
					_ = worker.Wait()
				}
			})
			deadline := time.Now().Add(5 * time.Second)
			for {
				procs, err := os.ReadFile(filepath.Join(directory, "cgroup.procs"))
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains("\n"+string(procs), "\n"+strconv.Itoa(worker.Process.Pid)+"\n") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("worker did not enter the CPU cgroup")
				}
				time.Sleep(time.Millisecond)
			}
			data, err := workloadLoadavg()
			if test.excluded {
				if err != nil || len(data) != 0 {
					t.Errorf("excluded workload metrics=%v err=%v", data, err)
				}
				return
			}
			if err != nil || len(data) != 2 {
				t.Errorf("load metrics=%v err=%v", data, err)
				return
			}
			prefix := "workload_"
			labels := map[string]string{"host": hostname, "region": metric.DefaultRegion(), "workload_id": "checkout-sandbox", "workload_type": test.kind, "workload_name": "checkout", "workload_namespace": "production"}
			if test.kind == pod.TypeContainer {
				prefix = "container_"
				labels = map[string]string{"host": hostname, "region": metric.DefaultRegion(), "container_host": "huatuo-dev", "container_name": "checkout", "container_hostnamespace": "production", "container_type": test.containerType.String(), "container_level": pod.ContainerQosLevelMin.String()}
			}
			for _, item := range data {
				if item.Type() != metric.MetricTypeGauge || !reflect.DeepEqual(item.Labels(), labels) {
					t.Errorf("metric %s type=%d labels=%v", item.Name(), item.Type(), item.Labels())
				}
				switch item.Name() {
				case prefix + "nr_running":
					if item.Value < 1 {
						t.Errorf("running task count=%v, want at least one", item.Value)
					}
				case prefix + "nr_uninterruptible":
					if item.Value != 0 {
						t.Errorf("uninterruptible task count=%v, want zero", item.Value)
					}
				default:
					t.Errorf("unexpected metric %s", item.Name())
				}
			}
			if err := worker.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = worker.Wait()
			data, err = workloadLoadavg()
			if err != nil || len(data) != 2 {
				t.Errorf("empty cgroup metrics=%v err=%v", data, err)
			}
			for _, item := range data {
				if item.Value != 0 {
					t.Errorf("empty cgroup %s=%v", item.Name(), item.Value)
				}
			}
			if err := os.Remove(directory); err != nil {
				t.Fatal(err)
			}
			data, err = (&loadavgCollector{}).Update()
			if err != nil || len(data) != 3 {
				t.Errorf("host-only metrics=%v err=%v", data, err)
			}
			for _, item := range data {
				if !strings.HasPrefix(item.Name(), "load") {
					t.Errorf("removed workload still emitted %s", item.Name())
				}
			}
		})
	}
}

// TestLoadavgUnavailableBinding 验证 unified、缺失 CPU 以及非 cgroup 目录不会
// 产生 workload 负载，也不会回退采集宿主机 CPU cgroup；Host 负载正常返回。
func TestLoadavgUnavailableBinding(t *testing.T) {
	for _, scenario := range []string{"unified", "no CPU", "ordinary directory"} {
		t.Run(scenario, func(t *testing.T) {
			if scenario == "no CPU" {
				startMemoryMetricFixture(t, pod.TypeCubeSandbox, true)
			} else {
				startCPUFixture(t, pod.TypeCubeSandbox, scenario == "ordinary directory", time.Hour)
			}
			data, err := workloadLoadavg()
			if err != nil || len(data) != 0 {
				t.Errorf("unsupported binding metrics=%v err=%v", data, err)
			}
			data, err = (&loadavgCollector{}).Update()
			if err != nil || len(data) != 3 {
				t.Errorf("host-only metrics=%v err=%v", data, err)
			}
		})
	}
}

func BenchmarkLoadavgWorkload(b *testing.B) {
	startLoadavgFixture(b, pod.TypeContainer, pod.ContainerTypeNormal, false)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		data, err := workloadLoadavg()
		if err != nil || len(data) != 2 {
			b.Fatalf("loadavg metrics=%v err=%v", data, err)
		}
	}
}
