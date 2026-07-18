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
	"huatuo-bamai/internal/server"
	profilecap "huatuo-bamai/pkg/profiling"
)

func TestGetFlameGraphURLEscapesLabelValue(t *testing.T) {
	cfg := config.Get()
	oldBase := cfg.Profiling.FlameGraphBaseURL
	cfg.Profiling.FlameGraphBaseURL = "http://grafana.example/d"
	defer func() { cfg.Profiling.FlameGraphBaseURL = oldBase }()

	url := getFlameGraphURL(&job.Job{
		Type:      ProfilingCPU,
		Host:      "node+2026&prod",
		Container: "container+2026&debug",
		StartTime: time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 6, 24, 10, 5, 0, 0, time.UTC),
	})

	if !strings.Contains(url, "var-container_hostname=container%2B2026%26debug") {
		t.Fatalf("url = %q, want escaped container label value", url)
	}
	if !strings.Contains(url, "var-hostname=node%2B2026%26prod") {
		t.Fatalf("url = %q, want escaped host label value for container dashboard", url)
	}
	if !strings.Contains(url, "/continuous-profiling-container/") ||
		!strings.Contains(url, "var-type=process_cpu%3Acpu%3Ananoseconds%3Acpu%3Ananoseconds") {
		t.Fatalf("url = %q, want provisioned dashboard UID and CPU profile type", url)
	}
}

func TestGetFlameGraphURLIncludesLockAndDimensionVariables(t *testing.T) {
	cfg := config.Get()
	oldBase := cfg.Profiling.FlameGraphBaseURL
	cfg.Profiling.FlameGraphBaseURL = "http://grafana.example/d"
	defer func() { cfg.Profiling.FlameGraphBaseURL = oldBase }()

	url := getFlameGraphURL(&job.Job{
		Type:      ProfilingLock,
		Host:      "node-a",
		StartTime: time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 7, 16, 10, 5, 0, 0, time.UTC),
		PrivateData: map[string]any{
			"scope":            "process-group",
			"cpu_ids":          []int{0, 1},
			"process_group_id": "321",
			"lock_mode":        "count",
		},
	})
	for _, expected := range []string{
		"/continuous-profiling-host/",
		"var-type=process_lock%3Alock%3Acount%3Alock%3Acount",
		"var-profiling_scope=process-group",
		"var-cpu=0%2C1",
		"var-process_group_id=321",
	} {
		if !strings.Contains(url, expected) {
			t.Fatalf("url = %q, want %q", url, expected)
		}
	}
}

func TestNewHandlerRegistersPyroscopeDiffRoute(t *testing.T) {
	handler := NewHandler(nil)
	want := map[string]int{
		"/flamegraph/querier.v1.QuerierService/Diff": server.HttpPost,
		"/flamegraph/export/pprof":                   server.HttpGet,
		"/flamegraph/export/svg":                     server.HttpGet,
	}
	for _, route := range handler.Handlers {
		if typ, ok := want[route.Uri]; ok && route.Typ == typ {
			delete(want, route.Uri)
		}
	}
	if len(want) != 0 {
		t.Fatalf("NewHandler() does not register routes: %v", want)
	}
}

func TestAppendCollectionTracerArgs(t *testing.T) {
	req := v1.StartProfilingRequest{
		Type:                  "cpu",
		TargetProcessLanguage: "go",
		CPUIds:                []int{3, 1},
		Scope:                 "process-group",
		ProcessGroupID:        321,
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
		"--cpuid", "1,3",
		"--scope", "process-group",
		"--process-group-id", "321",
		"--label", "service=checkout",
		"--label", "zone=cn-sh",
	}
	if !reflect.DeepEqual(taskReq.TracerArgs, want) {
		t.Fatalf("TracerArgs = %v, want %v", taskReq.TracerArgs, want)
	}
}

func TestAppendCollectionTracerArgsRejectsInvalidCPUIds(t *testing.T) {
	for _, cpuIDs := range [][]int{{-1}, {2, 2}} {
		taskReq := job.NewAgentTaskReq{}
		_, err := appendCollectionTracerArgs(&taskReq, &v1.StartProfilingRequest{
			Type: "cpu", TargetProcessLanguage: "go", CPUIds: cpuIDs,
		})
		if err == nil {
			t.Fatalf("CPUIds %v: expected validation error", cpuIDs)
		}
	}
}

func TestAppendCollectionTracerArgsRejectsCPUIdsForManagedRuntimes(t *testing.T) {
	for _, language := range []string{"java", "python"} {
		taskReq := job.NewAgentTaskReq{}
		_, err := appendCollectionTracerArgs(&taskReq, &v1.StartProfilingRequest{
			Type: "cpu", TargetProcessLanguage: language, CPUIds: []int{0},
		})
		if err == nil || !strings.Contains(err.Error(), "native") {
			t.Fatalf("language %q: error = %v, want native CPU filter error", language, err)
		}
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
		"cpu_ids":          []int{1, 3},
		"scope":            "cgroup",
		"pid":              "42",
		"cgroup_id":        "18446744073709551614",
		"cgroup_path":      "/sys/fs/cgroup/workload",
		"process_group_id": "321",
		"lock_types":       []string{"mutex", "rwlock"},
		"lock_mode":        "time",
		"lock_min_wait":    "10us",
		"labels":           map[string]string{"service": "checkout"},
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
	if !reflect.DeepEqual(got.CPUIds, []int{1, 3}) {
		t.Fatalf("CPUIds = %v", got.CPUIds)
	}
	if !reflect.DeepEqual(got.LockTypes, []string{"mutex", "rwlock"}) || got.LockMode != "time" {
		t.Fatalf("lock response = %+v", got)
	}
	if got.LockMinWait != "10us" || !reflect.DeepEqual(got.Labels, map[string]string{"service": "checkout"}) {
		t.Fatalf("lock metadata response = %+v", got)
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
	cfg.Profiling.MaxProfilerProcesses = 5
	defer func() { cfg.Profiling = old }()

	h := &Handler{}
	resp, err := buildCapabilitiesResponse(h)
	if err != nil {
		t.Fatalf("buildCapabilitiesResponse() error = %v", err)
	}

	if want := []string{"cpu", "memory", "lock"}; !reflect.DeepEqual(resp.ProfileTypes, want) {
		t.Errorf("ProfileTypes = %v, want %v", resp.ProfileTypes, want)
	}
	if want := []string{"cpu", "pid", "tgid", "cgroup", "process-group"}; !reflect.DeepEqual(resp.CollectionDimensions, want) {
		t.Errorf("CollectionDimensions = %v, want %v", resp.CollectionDimensions, want)
	}
	if want := []string{"mutex", "spinlock", "rwlock"}; !reflect.DeepEqual(resp.KernelLockTypes, want) {
		t.Errorf("KernelLockTypes = %v, want %v", resp.KernelLockTypes, want)
	}
	if want := []string{"c", "c++", "go"}; !reflect.DeepEqual(resp.LockSupportedLanguages, want) {
		t.Errorf("LockSupportedLanguages = %v, want %v", resp.LockSupportedLanguages, want)
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
	if resp.MaxProfilerProcesses != 5 {
		t.Errorf("MaxProfilerProcesses = %d, want 5", resp.MaxProfilerProcesses)
	}
}

func TestCapabilitiesReturnsIndependentMemoryModeMap(t *testing.T) {
	h := &Handler{}
	resp, _ := buildCapabilitiesResponse(h)
	resp.MemoryModes["NEW_MODE"] = "new_mode"
	resp.MemoryModes["NATIVE_PHYSICAL_ALLOC"] = "modified"

	next, _ := buildCapabilitiesResponse(h)
	if next.MemoryModes["NATIVE_PHYSICAL_ALLOC"] != "physical_alloc" {
		t.Errorf("MemoryModes was mutated across responses")
	}
	if _, ok := next.MemoryModes["NEW_MODE"]; ok {
		t.Errorf("MemoryModes retained a caller mutation")
	}
}

func TestFillMemoryTracerArgsUsesMemoryModeFlag(t *testing.T) {
	req := &job.NewAgentTaskReq{}

	err := fillMemoryTracerArgs(req, "", "NATIVE_PHYSICAL_USAGE", "")
	if err != nil {
		t.Fatalf("fillMemoryTracerArgs() error = %v", err)
	}

	want := []string{"-t", "memory", "--memory-mode", "physical_usage", "-l", "c"}
	if strings.Join(req.TracerArgs, " ") != strings.Join(want, " ") {
		t.Fatalf("TracerArgs = %q, want %q", req.TracerArgs, want)
	}
}

func TestFillProfilerArgsHonorImplementationRequirements(t *testing.T) {
	t.Run("native CPU rejects binary path", func(t *testing.T) {
		req := &job.NewAgentTaskReq{}
		err := fillCPUTracerArgs(req, "/bin/app", "c", "")
		if err == nil || !strings.Contains(err.Error(), "not supported for native") {
			t.Fatalf("fillCPUTracerArgs() error = %v", err)
		}
	})

	t.Run("Java CPU includes tool and binary paths", func(t *testing.T) {
		req := &job.NewAgentTaskReq{}
		err := fillCPUTracerArgs(req, "/usr/bin/java", "java", "/opt/async-profiler")
		if err != nil {
			t.Fatalf("fillCPUTracerArgs() error = %v", err)
		}
		want := []string{
			"-t", "cpu", "--binary-match-path", "/usr/bin/java", "-l", "java",
			"--tool-path", "/opt/async-profiler",
		}
		if !reflect.DeepEqual(req.TracerArgs, want) {
			t.Fatalf("TracerArgs = %v, want %v", req.TracerArgs, want)
		}
	})

	t.Run("Java memory requires configured tool path", func(t *testing.T) {
		err := fillMemoryTracerArgs(&job.NewAgentTaskReq{}, "java", "OBJECT_ALLOC", "")
		if err == nil || !strings.Contains(err.Error(), "configured tool path") {
			t.Fatalf("fillMemoryTracerArgs() error = %v", err)
		}
	})
}

func TestAppendProfilingTimingArgsUsesConfiguredInterval(t *testing.T) {
	req := &job.NewAgentTaskReq{Interval: 15}
	appendProfilingTimingArgs(req, 60)
	want := []string{"--duration", "15", "--aggr-interval", "15"}
	if req.Duration != 120 || !reflect.DeepEqual(req.TracerArgs, want) {
		t.Fatalf("task = %+v, want duration=120 args=%v", req, want)
	}
}

func TestValidateProfilingTarget(t *testing.T) {
	tests := []struct {
		name           string
		req            v1.StartProfilingRequest
		implementation profilecap.Implementation
		wantErr        bool
	}{
		{name: "Java PID", req: v1.StartProfilingRequest{PID: 1}, implementation: profilecap.ImplementationJava},
		{name: "Java missing target", implementation: profilecap.ImplementationJava, wantErr: true},
		{name: "Java duplicate target", req: v1.StartProfilingRequest{PID: 1, Container: "app"}, implementation: profilecap.ImplementationJava, wantErr: true},
		{name: "Java native dimension", req: v1.StartProfilingRequest{PID: 1, Scope: "tgid"}, implementation: profilecap.ImplementationJava, wantErr: true},
		{name: "Java CPU dimension", req: v1.StartProfilingRequest{PID: 1, CPUIds: []int{0}}, implementation: profilecap.ImplementationJava, wantErr: true},
		{name: "native memory PID", req: v1.StartProfilingRequest{Type: "memory", PID: 1}, implementation: profilecap.ImplementationNative},
		{name: "native memory cgroup", req: v1.StartProfilingRequest{Type: "memory", CgroupID: 2}, implementation: profilecap.ImplementationNative},
		{name: "native memory missing target", req: v1.StartProfilingRequest{Type: "memory"}, implementation: profilecap.ImplementationNative, wantErr: true},
		{name: "native memory duplicate target", req: v1.StartProfilingRequest{Type: "memory", PID: 1, ProcessGroupID: 2}, implementation: profilecap.ImplementationNative, wantErr: true},
		{name: "native CPU all", req: v1.StartProfilingRequest{Type: "cpu"}, implementation: profilecap.ImplementationNative},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateProfilingTarget(&tc.req, tc.implementation)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateProfilingTarget() error = %v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}
