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
	"testing"

	"huatuo-bamai/internal/bpf/abi"
	"huatuo-bamai/internal/pod"
)

// Regression: when cgroups.NewManager() fails (e.g. cgroupfs unavailable,
// cgroup mode unsupported), the oom collector keeps running with a nil
// manager, and the first container OOM panicked in cgroupMemorySnapshot
// instead of degrading to a snapshot-less event.
func TestCgroupMemorySnapshotNilManagerReturnsError(t *testing.T) {
	snapshot, err := cgroupMemorySnapshot(nil, &pod.Container{CgroupPath: "/kubepods/podabc/containerdef"})
	if err == nil {
		t.Fatalf("cgroupMemorySnapshot() err=nil, want error for nil cgroup manager")
	}
	if snapshot != nil {
		t.Errorf("cgroupMemorySnapshot() snapshot=%+v, want nil", snapshot)
	}
}

func TestBuildTracingDataWithoutCgroupManagerDegrades(t *testing.T) {
	containers := map[string]*pod.Container{
		"containerabc": {
			ID:         "containerabc",
			CgroupPath: "/kubepods/podabc/containerabc",
			CgroupCss:  map[string]uint64{"memory": 0x1234},
		},
	}

	data := buildTracingData(abi.OOMEvent{
		TriggerMemcgCSS: 0x1234,
		VictimMemcgCSS:  0x1234,
	}, containers, nil)

	if data == nil {
		t.Fatalf("buildTracingData()=nil, want an event without cgroup snapshots")
	}
	if data.Trigger.Cgroup != nil || data.Victim.Cgroup != nil {
		t.Errorf("buildTracingData() trigger/victim cgroup snapshots = %+v/%+v, want nil",
			data.Trigger.Cgroup, data.Victim.Cgroup)
	}
	if data.Victim.ContainerID != "containerabc" {
		t.Errorf("buildTracingData() victim container = %q, want %q",
			data.Victim.ContainerID, "containerabc")
	}
}
