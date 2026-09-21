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
	"testing"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/pkg/metric"
)

func reclaimMapItem(t *testing.T, cssAddr, stalls uint64) bpf.MapItem {
	t.Helper()

	key := make([]byte, 8)
	binary.LittleEndian.PutUint64(key, cssAddr)

	value := make([]byte, 8)
	binary.LittleEndian.PutUint64(value, stalls)

	return bpf.MapItem{Key: key, Value: value}
}

func reclaimTestContainers() map[uint64]*pod.Container {
	newContainer := func(id, name string) *pod.Container {
		return &pod.Container{
			ID:       id,
			Name:     name,
			Hostname: name,
			Type:     pod.ContainerTypeNormal,
			Labels:   map[string]any{"HostNamespace": "default"},
		}
	}

	return map[uint64]*pod.Container{
		0x1000: newContainer("aaaaaaaaaaaa", "reclaimer"),
		0x2000: newContainer("bbbbbbbbbbbb", "quiet"),
	}
}

func summarizeReclaimMetrics(t *testing.T, metrics []*metric.Data) map[string]float64 {
	t.Helper()

	byContainer := make(map[string]float64, len(metrics))
	for _, m := range metrics {
		if m.Name() != "container_directstall" {
			t.Fatalf("unexpected metric %q in the update result", m.Name())
		}
		byContainer[m.Labels()["container_name"]] = m.Value
	}
	return byContainer
}

func TestMemoryReclaimReportsZeroForContainersWithoutEvents(t *testing.T) {
	// Only the reclaimer has stall events; the quiet container must keep its
	// zero series instead of disappearing from the scrape.
	items := []bpf.MapItem{reclaimMapItem(t, 0x1000, 7)}

	metrics, err := buildReclaimMetrics(items, reclaimTestContainers())
	if err != nil {
		t.Fatalf("build reclaim metrics: %v", err)
	}

	got := summarizeReclaimMetrics(t, metrics)
	want := map[string]float64{"reclaimer": 7, "quiet": 0}
	for name, wantValue := range want {
		if got[name] != wantValue {
			t.Errorf("container %q directstall = %v, want %v", name, got[name], wantValue)
		}
	}
	if len(got) != len(want) {
		t.Errorf("reported containers = %v, want %v", got, want)
	}
}

func TestMemoryReclaimReportsZeroForAllContainersWhenMapEmpty(t *testing.T) {
	metrics, err := buildReclaimMetrics(nil, reclaimTestContainers())
	if err != nil {
		t.Fatalf("build reclaim metrics: %v", err)
	}

	got := summarizeReclaimMetrics(t, metrics)
	want := map[string]float64{"reclaimer": 0, "quiet": 0}
	for name, wantValue := range want {
		if got[name] != wantValue {
			t.Errorf("container %q directstall = %v, want %v", name, got[name], wantValue)
		}
	}
	if len(got) != len(want) {
		t.Errorf("reported containers = %v, want %v", got, want)
	}
}

func TestMemoryReclaimIgnoresEntriesWithoutContainer(t *testing.T) {
	// 0x3000 belongs to a cgroup that is no longer tracked as a container.
	items := []bpf.MapItem{
		reclaimMapItem(t, 0x3000, 3),
		reclaimMapItem(t, 0x1000, 5),
	}

	metrics, err := buildReclaimMetrics(items, reclaimTestContainers())
	if err != nil {
		t.Fatalf("build reclaim metrics: %v", err)
	}

	got := summarizeReclaimMetrics(t, metrics)
	want := map[string]float64{"reclaimer": 5, "quiet": 0}
	for name, wantValue := range want {
		if got[name] != wantValue {
			t.Errorf("container %q directstall = %v, want %v", name, got[name], wantValue)
		}
	}
	if len(got) != len(want) {
		t.Errorf("reported containers = %v, want %v", got, want)
	}
}
