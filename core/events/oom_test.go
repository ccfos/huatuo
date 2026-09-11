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
	"errors"
	"testing"

	"huatuo-bamai/internal/pod"
)

func TestOOMCollectorUpdatePrunesStaleContainerCounters(t *testing.T) {
	savedCounters := outOfMemoryCounterContainer
	savedHost := outOfMemoryCounterHost
	t.Cleanup(func() {
		outOfMemoryCounterContainer = savedCounters
		outOfMemoryCounterHost = savedHost
	})
	outOfMemoryCounterContainer = make(map[string]*oomMetric)
	outOfMemoryCounterHost = 0

	live := &pod.Container{
		ID:       "aaaaaaaaaaaa",
		Name:     "live",
		Hostname: "live",
		Type:     pod.ContainerTypeNormal,
		Labels:   map[string]any{"HostNamespace": "default"},
	}
	collector := &oomCollector{
		normalContainers: func() (map[string]*pod.Container, error) {
			return map[string]*pod.Container{live.ID: live}, nil
		},
	}

	containerCounterUpdate(live.ID, "java")
	containerCounterUpdate("bbbbbbbbbbbb", "python") // its container is already gone

	metrics, err := collector.Update()
	if err != nil {
		t.Fatalf("update oom metrics: %v", err)
	}

	mutex.Lock()
	_, staleExists := outOfMemoryCounterContainer["bbbbbbbbbbbb"]
	liveEntry, liveExists := outOfMemoryCounterContainer[live.ID]
	counterSize := len(outOfMemoryCounterContainer)
	mutex.Unlock()

	if staleExists {
		t.Error("counter of the deleted container was not pruned")
	}
	if !liveExists {
		t.Fatal("counter of the live container was pruned")
	}
	if counterSize != 1 {
		t.Errorf("container counter map size = %d, want 1", counterSize)
	}
	if liveEntry.count != 1 || liveEntry.latestVictimComm != "java" {
		t.Errorf("live counter = (%d, %q), want (1, %q)", liveEntry.count, liveEntry.latestVictimComm, "java")
	}

	// Counters of surviving containers keep accumulating across scrapes.
	containerCounterUpdate(live.ID, "go")
	mutex.Lock()
	count := outOfMemoryCounterContainer[live.ID].count
	mutex.Unlock()
	if count != 2 {
		t.Errorf("live counter after one more OOM = %d, want 2", count)
	}

	var totals, hostTotals int
	var totalValue float64
	var totalComm string
	for _, m := range metrics {
		switch m.Name() {
		case "container_total":
			totals++
			totalValue = m.Value
			totalComm = m.Labels()["latest_victim_comm"]
		case "host_total":
			hostTotals++
		}
	}
	if totals != 1 {
		t.Fatalf("container total metrics = %d, want 1", totals)
	}
	if totalValue != 1 {
		t.Errorf("container total = %v, want 1", totalValue)
	}
	if totalComm != "java" {
		t.Errorf("latest_victim_comm label = %q, want %q", totalComm, "java")
	}
	if hostTotals != 1 {
		t.Errorf("host_total metrics = %d, want 1", hostTotals)
	}
}

func TestOOMCollectorUpdatePropagatesContainerListFailure(t *testing.T) {
	collector := &oomCollector{
		normalContainers: func() (map[string]*pod.Container, error) {
			return nil, errors.New("kubelet unreachable")
		},
	}

	if _, err := collector.Update(); err == nil {
		t.Fatal("update succeeded, want the container list error to propagate")
	}
}
