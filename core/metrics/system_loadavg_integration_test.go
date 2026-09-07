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

//go:build integration && linux

package collector

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/paths"
	"github.com/ccfos/huatuo/internal/cgroups/stats"
	cgroupV2 "github.com/ccfos/huatuo/internal/cgroups/v2"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/pkg/metric"

	"github.com/google/cadvisor/utils/cpuload/netlink"
)

// Run only against an explicitly selected v1 CPU hierarchy. Workers and their
// cgroup are removed even if iterator probing or netlink collection fails.
func TestLoadavgLiveV1Sampling(t *testing.T) {
	root := os.Getenv("HUATUO_LOADAVG_V1_ROOT")
	if root == "" || os.Geteuid() != 0 {
		t.Skip("requires root and HUATUO_LOADAVG_V1_ROOT=/sys/fs/cgroup/cpu")
	}
	previousRoot := paths.RootfsDefaultPath
	paths.RootfsDefaultPath = filepath.Dir(root)
	t.Cleanup(func() { paths.RootfsDefaultPath = previousRoot })
	dir, err := os.MkdirTemp(root, "huatuo-loadavg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(dir); err != nil {
			t.Error(err)
		}
	})
	worker := exec.Command("sh", "-c", "while :; do :; done")
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worker.Process.Kill(); _ = worker.Wait() })
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(strconv.Itoa(worker.Process.Pid)), 0); err != nil {
		t.Fatal(err)
	}
	n, err := netlink.New()
	if err != nil {
		t.Fatal(err)
	}
	defer n.Stop()
	container := vmstatTestContainer("")
	container.CgroupPath = filepath.Base(dir)
	readV1 := func() ([]containerLoadSample, error) {
		return readContainerLoadSamplesV1(map[string]*pod.Container{"fixture": container}, n.GetCpuLoad)
	}
	first, err := readV1()
	if err != nil || len(first) != 1 || first[0].running != 1 {
		t.Fatalf("live v1 sample=%+v, err=%v", first, err)
	}
	readHost := func(bool) ([]containerLoadSample, *stats.LoadStats, error) {
		_, host, err := cgroupV2.SharedLoadStatsWithHost(cgroupV2.LoadStatsConsumerLoadavg, nil)
		return nil, host, err
	}
	t.Cleanup(func() { _ = cgroupV2.CloseLoadStats() })
	expectUnsupported := os.Getenv("HUATUO_LOADAVG_EXPECT_UNSUPPORTED") == "1"
	if expectUnsupported {
		_, host, err := readHost(false)
		if host != nil || !errors.Is(err, cgroupV2.ErrTaskIteratorNotSupported) {
			t.Fatalf("expected unsupported iterator, got host=%v err=%v", host, err)
		}
		t.Logf("confirmed real iterator fallback: %v", err)
	}
	c := &loadavgCollector{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- c.sampleLoad(ctx, func() ([]containerLoadSample, *stats.LoadStats, error) {
			return c.readLoadSample(cgroups.Hybrid, readV1, readHost)
		})
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	deadline := time.Now().Add(c.samplingInterval() + 5*time.Second)
	for time.Now().Before(deadline) {
		data, err, active := c.cachedContainerLoad(time.Now())
		if err != nil {
			t.Fatal(err)
		}
		values := loadMetricValues(data)
		if active && values["container_load1"] > 0 {
			if !(values["container_load1"] > values["container_load5"] && values["container_load5"] > values["container_load15"] && values["container_load15"] > 0) {
				t.Fatalf("invalid averages: %v", values)
			}
			all, err := c.Update()
			if err != nil {
				t.Fatal(err)
			}
			allValues := loadMetricValues(all)
			for _, name := range []string{"load1", "load5", "load15", "nr_running", "container_nr_running", "container_load1", "container_load5", "container_load15"} {
				if _, ok := allValues[name]; !ok {
					t.Fatalf("missing %s after real sampling", name)
				}
			}
			if expectUnsupported {
				if _, ok := allValues["nr_uninterruptible"]; ok {
					t.Fatal("unsupported host iterator exported zero")
				}
			}
			t.Logf("live %s sample and host metrics: %v", c.samplingInterval(), allValues)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("no container averages after a live sampling interval")
}

func vmstatTestContainer(root string) *pod.Container {
	return &pod.Container{
		ID: "vmstat-test", Name: "workload", CgroupPath: root,
		Type:   pod.ContainerTypeNormal,
		Labels: map[string]any{"HostNamespace": "test"},
	}
}

func loadMetricValues(data []*metric.Data) map[string]float64 {
	values := make(map[string]float64, len(data))
	for _, d := range data {
		values[d.Name()] = d.Value
	}
	return values
}
