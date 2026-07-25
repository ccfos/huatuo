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
	"slices"
	"strings"
	"testing"
	"time"

	v1 "huatuo-bamai/apis/v1"
	"huatuo-bamai/internal/job"
	profileService "huatuo-bamai/internal/profiler/service"
	profiletypes "huatuo-bamai/pkg/profiling"
)

func TestGetFlameGraphURLEscapesLabelValue(t *testing.T) {
	url := getFlameGraphURL("http://grafana.example/d", &job.Job{
		Type:        ProfilingCPU,
		ContainerID: "container+2026&debug",
		StartTime:   time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC),
		EndTime:     time.Date(2026, 6, 24, 10, 5, 0, 0, time.UTC),
	})

	if !strings.Contains(url, "var-container_id=container%2B2026%26debug") {
		t.Fatalf("url = %q, want escaped container label value", url)
	}
}

func TestNewHandlerSnapshotsProfilingConfig(t *testing.T) {
	cfg := Config{AggregationInterval: 15}
	h := NewHandler(nil, nil, cfg)
	cfg.AggregationInterval = 30

	if h.profilingConfig.AggregationInterval != 15 {
		t.Fatalf(
			"AggregationInterval = %d, want 15",
			h.profilingConfig.AggregationInterval,
		)
	}
}

// TestCapabilities verifies that the capabilities handler returns the correct
// profiling types, languages, memory modes, and default configuration values.
func TestCapabilities(t *testing.T) {
	h := &Handler{profilingConfig: Config{
		AggregationInterval: 15,
		ExecutionTimeout:    30,
		MaxProfilerProcs:    5,
	}}
	resp := buildCapabilitiesResponse(h)

	if len(resp.Types) != 3 {
		t.Errorf("Types len = %d, want 3", len(resp.Types))
	}
	hasCPU := false
	hasMemory := false
	hasLock := false
	for _, pt := range resp.Types {
		if pt == "cpu" {
			hasCPU = true
		}
		if pt == "memory" {
			hasMemory = true
		}
		if pt == "lock" {
			hasLock = true
		}
	}
	if !hasCPU || !hasMemory || !hasLock {
		t.Errorf("Types = %v, want contain cpu, memory, and lock", resp.Types)
	}

	if len(resp.CPULanguages) != 5 {
		t.Errorf("CPULanguages len = %d, want 5 (c++, c, go, java, python)", len(resp.CPULanguages))
	}
	hasPython := false
	for _, lang := range resp.CPULanguages {
		if lang == "python" {
			hasPython = true
		}
	}
	if !hasPython {
		t.Errorf("CPULanguages = %v, want contain python", resp.CPULanguages)
	}

	if len(resp.MemoryLanguages) != 4 {
		t.Errorf("MemoryLanguages len = %d, want 4 (c++, c, go, java)", len(resp.MemoryLanguages))
	}
	if len(resp.LockLanguages) != 3 {
		t.Errorf("LockLanguages len = %d, want 3 (c++, c, go)", len(resp.LockLanguages))
	}
	if !slices.Equal(resp.LockModes, []profiletypes.LockMode{profiletypes.LockModeWaitTime}) {
		t.Errorf("LockModes = %v, want wait_time", resp.LockModes)
	}
	if !slices.Equal(resp.LockTypes, []profiletypes.LockType{
		profiletypes.LockTypeMutex,
		profiletypes.LockTypeSpinlock,
		profiletypes.LockTypeRWLock,
	}) {
		t.Errorf("LockTypes = %v, want mutex, spinlock, and rwlock", resp.LockTypes)
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

	if resp.AggregationInterval != 15 {
		t.Errorf("AggregationInterval = %d, want 15", resp.AggregationInterval)
	}
	if resp.ExecutionTimeout != 30 {
		t.Errorf("ExecutionTimeout = %d, want 30", resp.ExecutionTimeout)
	}
	if resp.MaxProfilerProcs != 5 {
		t.Errorf("MaxProfilerProcs = %d, want 5", resp.MaxProfilerProcs)
	}
}

func TestCapabilitiesReturnsIndependentMemoryModeMap(t *testing.T) {
	h := &Handler{}
	resp := buildCapabilitiesResponse(h)
	resp.MemoryModes["NEW_MODE"] = "new_mode"
	resp.MemoryModes["NATIVE_PHYSICAL_ALLOC"] = "modified"

	next := buildCapabilitiesResponse(h)
	if next.MemoryModes["NATIVE_PHYSICAL_ALLOC"] != "physical_alloc" {
		t.Errorf("MemoryModes was mutated across responses")
	}
	if _, ok := next.MemoryModes["NEW_MODE"]; ok {
		t.Errorf("MemoryModes retained a caller mutation")
	}
}

func TestProfilingPrivateDataUsesRequestJSONNames(t *testing.T) {
	data, err := newProfilingPrivateData(&v1.CreateProfilingJobRequest{
		BinaryMatchPath:   "/usr/bin/example",
		ToolPath:          "/opt/profiler",
		Duration:          60,
		Language:          "go",
		MemoryMode:        "object_alloc",
		CPUIDs:            []int{1, 3},
		PID:               4242,
		ThreadGroup:       true,
		LockMode:          profiletypes.LockModeWaitTime,
		LockType:          profiletypes.LockTypeMutex,
		LockWaitThreshold: "10us",
	})
	if err != nil {
		t.Fatalf("newProfilingPrivateData() error=%v", err)
	}

	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("json.Unmarshal() error=%v", err)
	}
	if fields["binary_match_path"] != "/usr/bin/example" ||
		fields["tool_path"] != "/opt/profiler" ||
		fields["duration"] != float64(60) ||
		fields["language"] != "go" ||
		fields["memory_mode"] != "object_alloc" ||
		fields["pid"] != float64(4242) ||
		fields["thread_group"] != true ||
		fields["lock_mode"] != "wait_time" ||
		fields["lock_type"] != "mutex" ||
		fields["lock_wait_threshold"] != "10us" {
		t.Errorf("newProfilingPrivateData()=%s, want request fields", data)
	}
	cpuIDs, ok := fields["cpu_ids"].([]any)
	if !ok || len(cpuIDs) != 2 || cpuIDs[0] != float64(1) || cpuIDs[1] != float64(3) {
		t.Errorf("newProfilingPrivateData() cpu_ids=%v, want [1 3]", fields["cpu_ids"])
	}
}

func TestConvertJobToProfilingResponseReadsRequestJSONNames(t *testing.T) {
	resp, err := buildProfilingJobResponse(&job.Job{
		Type:   ProfilingCPU,
		Status: job.JobStatusRunning,
		PrivateData: json.RawMessage(`{
			"binary_match_path":"/usr/bin/example",
			"tool_path":"/opt/profiler",
			"duration":60,
			"language":"go",
			"memory_mode":"object_alloc",
			"cpu_ids":[1,3],
			"pid":4242,
			"thread_group":true
		}`),
	}, "")
	if err != nil {
		t.Fatalf("buildProfilingJobResponse() error = %v", err)
	}

	if resp.BinaryMatchPath != "/usr/bin/example" {
		t.Errorf("BinaryMatchPath=%q, want %q", resp.BinaryMatchPath, "/usr/bin/example")
	}
	if resp.ToolPath != "/opt/profiler" {
		t.Errorf("ToolPath=%q, want %q", resp.ToolPath, "/opt/profiler")
	}
	if resp.Language != "go" {
		t.Errorf("Language=%q, want %q", resp.Language, "go")
	}
	if resp.MemoryMode != "object_alloc" {
		t.Errorf("MemoryMode=%q, want %q", resp.MemoryMode, "object_alloc")
	}
	if resp.Duration != 60 {
		t.Errorf("Duration=%d, want 60", resp.Duration)
	}
	if len(resp.CPUIDs) != 2 || resp.CPUIDs[0] != 1 || resp.CPUIDs[1] != 3 {
		t.Errorf("CPUIDs=%v, want [1 3]", resp.CPUIDs)
	}
	if !resp.ThreadGroup {
		t.Error("ThreadGroup=false, want true")
	}
	if resp.PID != 4242 {
		t.Errorf("PID=%d, want 4242", resp.PID)
	}
}

func TestConvertLockJobToProfilingResponse(t *testing.T) {
	resp, err := buildProfilingJobResponse(&job.Job{
		Type:   ProfilingLock,
		Status: job.JobStatusRunning,
		PrivateData: json.RawMessage(`{
			"duration":60,
			"language":"go",
			"pid":4242,
			"lock_mode":"wait_time",
			"lock_type":"mutex",
			"lock_wait_threshold":"10us"
		}`),
	}, "")
	if err != nil {
		t.Fatalf("buildProfilingJobResponse() error = %v", err)
	}
	if resp.Type != string(profiletypes.TypeLock) {
		t.Errorf("Type=%q, want lock", resp.Type)
	}
	if resp.PID != 4242 ||
		resp.LockMode != profiletypes.LockModeWaitTime ||
		resp.LockType != profiletypes.LockTypeMutex ||
		resp.LockWaitThreshold != "10us" {
		t.Errorf("lock response fields = %+v, want persisted lock selection", resp)
	}
}

func TestProfilingJobResponseRejectsNonProfilingJob(t *testing.T) {
	_, err := buildProfilingJobResponse(&job.Job{Type: "trace"}, "")
	if err == nil {
		t.Fatal("buildProfilingJobResponse() error = nil, want non-nil")
	}
}

func TestProfilingJobResponseRejectsInvalidPrivateData(t *testing.T) {
	_, err := buildProfilingJobResponse(&job.Job{
		Type:        ProfilingCPU,
		PrivateData: json.RawMessage(`{"duration":`),
	}, "")
	if err == nil {
		t.Fatal("buildProfilingJobResponse() error = nil, want non-nil")
	}
}

func TestIsProfilingJobType(t *testing.T) {
	tests := []struct {
		name    string
		jobType job.JobType
		want    bool
	}{
		{name: "cpu profiling", jobType: ProfilingCPU, want: true},
		{name: "memory profiling", jobType: ProfilingMemory, want: true},
		{name: "lock profiling", jobType: ProfilingLock, want: true},
		{name: "trace job", jobType: job.JobType("trace"), want: false},
		{name: "empty type", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isProfilingJobType(tt.jobType); got != tt.want {
				t.Errorf("isProfilingJobType(%q)=%t, want %t", tt.jobType, got, tt.want)
			}
		})
	}
}

func TestProfilingJobResponseBuildsURLWithoutMutatingJob(t *testing.T) {
	jobResult := &job.Job{
		ID:          "profile-2026",
		Type:        ProfilingCPU,
		Hostname:    "huatuo-dev",
		Status:      job.JobStatusCompleted,
		StartTime:   time.Date(2026, time.July, 20, 10, 0, 0, 0, time.UTC),
		EndTime:     time.Date(2026, time.July, 20, 10, 1, 0, 0, time.UTC),
		PrivateData: json.RawMessage(`{"duration":60}`),
	}

	resp, err := buildProfilingJobResponse(jobResult, "http://grafana.example/d")
	if err != nil {
		t.Fatalf("buildProfilingJobResponse() error = %v", err)
	}
	if resp.Results.URL == "" {
		t.Error("buildProfilingJobResponse() result URL is empty")
	}
	if jobResult.Result.URL != "" {
		t.Errorf("job result URL mutated to %q", jobResult.Result.URL)
	}
	if resp.Duration != 60 {
		t.Errorf("Duration=%d, want 60", resp.Duration)
	}
}

func TestProfilingJobResponseFormatsZeroEndTimeAsEmpty(t *testing.T) {
	resp, err := buildProfilingJobResponse(&job.Job{Type: ProfilingCPU}, "")
	if err != nil {
		t.Fatalf("buildProfilingJobResponse() error = %v", err)
	}
	if resp.EndTime != "" {
		t.Errorf("EndTime=%q, want empty", resp.EndTime)
	}
}

func TestRawProfileResponsesMapsStableWireType(t *testing.T) {
	document := &profileService.ProfileDocument{
		Hostname:   "node-a",
		TracerID:   "trace-a",
		TracerTime: "2026-07-22T10:00:00Z",
	}
	document.TracerData.Flamedata.ProfileType = "process_cpu"

	items := rawProfileResponses([]*profileService.ProfileDocument{nil, document})
	if len(items) != 1 {
		t.Fatalf("rawProfileResponses() length = %d, want 1", len(items))
	}
	if items[0].Hostname != document.Hostname || items[0].TracerID != document.TracerID {
		t.Fatalf(
			"raw profile identity = (%q, %q), want document identity",
			items[0].Hostname,
			items[0].TracerID,
		)
	}
	if got := items[0].TracerData.Flamedata.ProfileType; got != "process_cpu" {
		t.Fatalf("profile type = %q, want process_cpu", got)
	}
}
