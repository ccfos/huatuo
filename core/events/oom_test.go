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
	"huatuo-bamai/internal/cgroups/subsystem"
	"huatuo-bamai/internal/pod"
)

func TestBuildOOMTracingDataCgroupKeys(t *testing.T) {
	data := abi.OOMEvent{}
	copy(data.TriggerComm[:], "trigger")
	copy(data.VictimComm[:], "victim")
	data.TriggerTGID = 11
	data.VictimTGID = 22
	data.TriggerCgroupKey.CgroupID = 101
	data.VictimCgroupKey.CSS = 202

	tracingData := buildTracingData(&data, map[string]*pod.Container{
		"v2": {ID: "v2", Hostname: "node-v2", CgroupID: 101},
		"v1": {
			ID:       "v1",
			Hostname: "node-v1",
			CgroupCss: map[string]uint64{
				subsystem.SubsystemMemory: 202,
			},
		},
	}, nil)
	if tracingData.Trigger.ContainerID != "v2" || tracingData.Trigger.Comm != "trigger" {
		t.Fatalf("trigger = %+v, want v2/trigger", tracingData.Trigger)
	}
	if tracingData.Victim.ContainerID != "v1" || tracingData.Victim.Comm != "victim" {
		t.Fatalf("victim = %+v, want v1/victim", tracingData.Victim)
	}
}

func TestBuildOOMTracingDataDropsDuplicateCgroupKey(t *testing.T) {
	data := abi.OOMEvent{}
	data.TriggerCgroupKey.CgroupID = 101 // shared by two containers: dropped
	data.VictimCgroupKey.CgroupID = 202

	tracingData := buildTracingData(&data, map[string]*pod.Container{
		"first":  {ID: "first", CgroupID: 101},
		"second": {ID: "second", CgroupID: 101},
		"third":  {ID: "third", CgroupID: 202},
	}, nil)
	if tracingData.Trigger.ContainerID != "" {
		t.Fatalf("trigger container ID = %q, want empty (host attribution)", tracingData.Trigger.ContainerID)
	}
	if tracingData.Victim.ContainerID != "third" {
		t.Fatalf("victim container ID = %q, want third", tracingData.Victim.ContainerID)
	}
}
