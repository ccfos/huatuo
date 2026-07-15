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

package profiling

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	v1 "huatuo-bamai/apis/v1"
	"huatuo-bamai/cmd/huatuo-apiserver/config"
	"huatuo-bamai/internal/job"
)

func TestGetFlameGraphURLEscapesLabelValue(t *testing.T) {
	cfg := config.Get()
	oldBase := cfg.Profiling.FlameGraphBaseURL
	cfg.Profiling.FlameGraphBaseURL = "http://grafana.example/d"
	defer func() { cfg.Profiling.FlameGraphBaseURL = oldBase }()

	url := getFlameGraphURL(&job.Job{
		Type:      ProfilingCPU,
		Container: "container+2026&debug",
		StartTime: time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 6, 24, 10, 5, 0, 0, time.UTC),
	})

	if !strings.Contains(url, "var-container_hostname=container%2B2026%26debug") {
		t.Fatalf("url = %q, want escaped container label value", url)
	}
}

func TestAppendCollectionTracerArgs(t *testing.T) {
	req := v1.StartProfilingRequest{
		Scope:          "process-group",
		ProcessGroupID: 321,
		Labels: map[string]string{
			"zone":    "cn-sh",
			"service": "checkout",
		},
	}
	taskReq := job.NewAgentTaskReq{}
	scope, err := appendCollectionTracerArgs(&taskReq, &req)
	if err != nil {
		t.Fatalf("appendCollectionTracerArgs() error = %v", err)
	}
	if scope != "process-group" {
		t.Fatalf("appendCollectionTracerArgs() scope = %q", scope)
	}
	want := []string{
		"--scope", "process-group",
		"--process-group-id", "321",
		"--label", "service=checkout",
		"--label", "zone=cn-sh",
	}
	if !reflect.DeepEqual(taskReq.TracerArgs, want) {
		t.Fatalf("TracerArgs = %v, want %v", taskReq.TracerArgs, want)
	}
}

func TestAppendCollectionTracerArgsInfersCanonicalTGIDScope(t *testing.T) {
	taskReq := job.NewAgentTaskReq{}
	scope, err := appendCollectionTracerArgs(&taskReq, &v1.StartProfilingRequest{PID: 123})
	if err != nil {
		t.Fatalf("appendCollectionTracerArgs() error = %v", err)
	}
	if scope != "tgid" {
		t.Fatalf("scope = %q, want tgid", scope)
	}
	want := []string{"--scope", "tgid", "--pid", "123"}
	if !reflect.DeepEqual(taskReq.TracerArgs, want) {
		t.Fatalf("TracerArgs = %v, want %v", taskReq.TracerArgs, want)
	}
}

func TestAppendCollectionTracerArgsRejectsReservedLabel(t *testing.T) {
	taskReq := job.NewAgentTaskReq{}
	_, err := appendCollectionTracerArgs(&taskReq, &v1.StartProfilingRequest{
		PID:    123,
		Labels: map[string]string{"cgroup_id": "forged"},
	})
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("appendCollectionTracerArgs() error = %v, want reserved label error", err)
	}
}

func TestConvertJobToProfilingResponseAfterPrivateDataJSONRoundTrip(t *testing.T) {
	privateData := map[string]any{
		"scope":            "cgroup",
		"pid":              "42",
		"cgroup_id":        "18446744073709551614",
		"cgroup_path":      "/sys/fs/cgroup/workload",
		"process_group_id": "321",
		"lock_types":       []string{"mutex", "rwlock"},
		"lock_mode":        "time",
	}
	raw, err := json.Marshal(privateData)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var roundTripped map[string]any
	if err := json.Unmarshal(raw, &roundTripped); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	got := (&Handler{}).convertJobToProfilingResponse(&job.Job{
		Type:        ProfilingLock,
		Status:      job.JobStatusPending,
		PrivateData: roundTripped,
	})
	if got.Scope != "cgroup" || got.PID != 42 || got.CgroupID != 18446744073709551614 ||
		got.CgroupPath != "/sys/fs/cgroup/workload" || got.ProcessGroupID != 321 {
		t.Fatalf("dimension response = %+v", got)
	}
	if !reflect.DeepEqual(got.LockTypes, []string{"mutex", "rwlock"}) || got.LockMode != "time" {
		t.Fatalf("lock response = %+v", got)
	}
}

func TestFillLockTracerArgs(t *testing.T) {
	taskReq := job.NewAgentTaskReq{}
	lockTypes, lockMode, err := fillLockTracerArgs(&taskReq, &v1.StartProfilingRequest{
		TargetProcessLanguage: "go",
		LockTypes:             []string{"Mutex", "rwlock", "mutex"},
		LockMode:              "count",
		LockMinWait:           "10us",
	})
	if err != nil {
		t.Fatalf("fillLockTracerArgs() error = %v", err)
	}
	want := []string{"-t", "lock", "-l", "go", "--lock-types", "mutex,rwlock", "--lock-mode", "count", "--lock-min-wait", "10us"}
	if !reflect.DeepEqual(taskReq.TracerArgs, want) {
		t.Fatalf("TracerArgs = %v, want %v", taskReq.TracerArgs, want)
	}
	if !reflect.DeepEqual(lockTypes, []string{"mutex", "rwlock"}) || lockMode != "count" {
		t.Fatalf("effective lock options = %v/%q", lockTypes, lockMode)
	}
}

func TestFillLockTracerArgsReturnsEffectiveDefaults(t *testing.T) {
	taskReq := job.NewAgentTaskReq{}
	lockTypes, lockMode, err := fillLockTracerArgs(&taskReq, &v1.StartProfilingRequest{
		TargetProcessLanguage: "c",
	})
	if err != nil {
		t.Fatalf("fillLockTracerArgs() error = %v", err)
	}
	if !reflect.DeepEqual(lockTypes, []string{"mutex", "spinlock", "rwlock"}) || lockMode != "time" {
		t.Fatalf("effective lock defaults = %v/%q", lockTypes, lockMode)
	}
}

// TestCapabilities verifies that the capabilities handler returns the correct
// profiling types, languages, memory modes, and default configuration values.
func TestCapabilities(t *testing.T) {
	cfg := config.Get()
	old := cfg.Profiling
	cfg.Profiling.CPUProfilingInterval = 15
	cfg.Profiling.MemoryProfilingInterval = 20
	cfg.Profiling.CPUSingleTraceTimeout = 30
	cfg.Profiling.MemorySingleTraceTimeout = 40
	cfg.Profiling.ThirdPartyToolLimit = 5
	defer func() { cfg.Profiling = old }()

	h := &Handler{}
	resp, err := buildCapabilitiesResponse(h)
	if err != nil {
		t.Fatalf("buildCapabilitiesResponse() error = %v", err)
	}

	if len(resp.ProfileTypes) != 3 {
		t.Errorf("ProfileTypes len = %d, want 3", len(resp.ProfileTypes))
	}
	hasCPU := false
	hasMemory := false
	for _, pt := range resp.ProfileTypes {
		if pt == "cpu" {
			hasCPU = true
		}
		if pt == "memory" {
			hasMemory = true
		}
	}
	if !hasCPU || !hasMemory {
		t.Errorf("ProfileTypes = %v, want contain both cpu and memory", resp.ProfileTypes)
	}
	if len(resp.CollectionDimensions) != 4 {
		t.Errorf("CollectionDimensions = %v", resp.CollectionDimensions)
	}
	if len(resp.KernelLockTypes) != 3 {
		t.Errorf("KernelLockTypes = %v", resp.KernelLockTypes)
	}

	if len(resp.CPUSupportedLanguages) != 5 {
		t.Errorf("CPUSupportedLanguages len = %d, want 5 (c++, c, go, java, python)", len(resp.CPUSupportedLanguages))
	}
	hasPython := false
	for _, lang := range resp.CPUSupportedLanguages {
		if lang == "python" {
			hasPython = true
		}
	}
	if !hasPython {
		t.Errorf("CPUSupportedLanguages = %v, want contain python", resp.CPUSupportedLanguages)
	}

	if len(resp.MemorySupportedLanguages) != 4 {
		t.Errorf("MemorySupportedLanguages len = %d, want 4 (c++, c, go, java)", len(resp.MemorySupportedLanguages))
	}

	if len(resp.MemoryModes) != 5 {
		t.Errorf("MemoryModes len = %d, want 5", len(resp.MemoryModes))
	}
	if _, ok := resp.MemoryModes["NATIVE_PHYSICAL_ALLOC"]; !ok {
		t.Errorf("MemoryModes missing NATIVE_PHYSICAL_ALLOC")
	}
	if _, ok := resp.MemoryModes["OBJECT_USAGE"]; !ok {
		t.Errorf("MemoryModes missing OBJECT_USAGE")
	}

	if resp.DefaultCPUInterval != 15 {
		t.Errorf("DefaultCPUInterval = %d, want 15", resp.DefaultCPUInterval)
	}
	if resp.DefaultMemoryInterval != 20 {
		t.Errorf("DefaultMemoryInterval = %d, want 20", resp.DefaultMemoryInterval)
	}
	if resp.DefaultCPUSingleTraceTimeout != 30 {
		t.Errorf("DefaultCPUSingleTraceTimeout = %d, want 30", resp.DefaultCPUSingleTraceTimeout)
	}
	if resp.DefaultMemorySingleTraceTimeout != 40 {
		t.Errorf("DefaultMemorySingleTraceTimeout = %d, want 40", resp.DefaultMemorySingleTraceTimeout)
	}
	if resp.ThirdPartyToolLimit != 5 {
		t.Errorf("ThirdPartyToolLimit = %d, want 5", resp.ThirdPartyToolLimit)
	}
}

// TestCapabilitiesDoesNotMutatePackageMaps verifies that calling capabilities
// does not mutate the package-level supportedLanguages or supportedMemoryMaps.
func TestCapabilitiesDoesNotMutatePackageMaps(t *testing.T) {
	h := &Handler{}
	_, _ = buildCapabilitiesResponse(h)

	resp, _ := buildCapabilitiesResponse(h)
	resp.MemoryModes["NEW_MODE"] = "new_mode"
	resp.MemoryModes["NATIVE_PHYSICAL_ALLOC"] = "modified"

	_, ok := supportedMemoryModes["NATIVE_PHYSICAL_ALLOC"]
	if !ok || supportedMemoryModes["NATIVE_PHYSICAL_ALLOC"] != "native_physical_alloc" {
		t.Errorf("supportedMemoryModes was mutated by capabilities response")
	}

	_, ok = supportedMemoryModes["NEW_MODE"]
	if ok {
		t.Errorf("supportedMemoryModes was mutated with NEW_MODE")
	}
}
