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
	"strings"
	"testing"
	"time"

	v1 "huatuo-bamai/apis/v1"
	"huatuo-bamai/internal/job"
	profileService "huatuo-bamai/internal/profiler/service"
)

func TestNewHandlerOmitsStorageRoutesWhenDisabled(t *testing.T) {
	handler := NewHandler(nil, nil, Config{})

	for _, route := range handler.Handlers {
		if route.Uri == "/:id/raw" || strings.HasPrefix(route.Uri, "/flamegraph/") {
			t.Errorf("storage route %q registered without profile storage", route.Uri)
		}
	}
}

func TestNewHandlerRegistersStorageRoutesWhenEnabled(t *testing.T) {
	handler := NewHandler(nil, &profileService.Service{}, Config{})
	routes := make(map[string]struct{}, len(handler.Handlers))
	for _, route := range handler.Handlers {
		routes[route.Uri] = struct{}{}
	}

	want := []string{
		"/:id/raw",
		"/flamegraph/querier.v1.QuerierService/SelectMergeStacktraces",
		"/flamegraph/querier.v1.QuerierService/ProfileTypes",
		"/flamegraph/querier.v1.QuerierService/LabelNames",
		"/flamegraph/querier.v1.QuerierService/LabelValues",
	}
	for _, route := range want {
		if _, ok := routes[route]; !ok {
			t.Errorf("storage route %q is not registered", route)
		}
	}
}

func TestGetFlameGraphURLEscapesLabelValue(t *testing.T) {
	url := getFlameGraphURL("http://grafana.example/d", &job.Job{
		Type:        ProfilingCPU,
		ContainerID: "container+2026&debug",
		CreatedAt:   time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC),
		FinishedAt:  time.Date(2026, 6, 24, 10, 5, 0, 0, time.UTC),
	})

	if !strings.Contains(url, "var-container_id=container%2B2026%26debug") {
		t.Fatalf("url = %q, want escaped container label value", url)
	}
}

func TestNewHandlerSnapshotsProfilingConfig(t *testing.T) {
	cfg := Config{AggregationIntervalSeconds: 15}
	h := NewHandler(nil, nil, cfg)
	cfg.AggregationIntervalSeconds = 30

	if h.profilingConfig.AggregationIntervalSeconds != 15 {
		t.Fatalf(
			"AggregationIntervalSeconds = %d, want 15",
			h.profilingConfig.AggregationIntervalSeconds,
		)
	}
}

// TestCapabilities verifies that the capabilities handler returns the correct
// profiling types, languages, memory modes, and default configuration values.
func TestCapabilities(t *testing.T) {
	h := &Handler{profilingConfig: Config{
		AggregationIntervalSeconds:     15,
		MaxConcurrentProfilerProcesses: 5,
	}}
	resp := buildCapabilities(h)

	if len(resp.Types) != 2 {
		t.Errorf("Types len = %d, want 2", len(resp.Types))
	}
	hasCPU := false
	hasMemory := false
	for _, pt := range resp.Types {
		if pt == "cpu" {
			hasCPU = true
		}
		if pt == "memory" {
			hasMemory = true
		}
	}
	if !hasCPU || !hasMemory {
		t.Errorf("Types = %v, want contain both cpu and memory", resp.Types)
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

	if len(resp.MemoryModes) != 4 {
		t.Errorf("MemoryModes len = %d, want 4", len(resp.MemoryModes))
	}
	if len(resp.MemoryModes["go"]) != 3 {
		t.Errorf("MemoryModes[go] = %v, want 3 modes", resp.MemoryModes["go"])
	}
	if len(resp.MemoryModes["java"]) != 2 {
		t.Errorf("MemoryModes[java] = %v, want 2 modes", resp.MemoryModes["java"])
	}

	if resp.AggregationIntervalSeconds != 15 {
		t.Errorf("AggregationIntervalSeconds = %d, want 15", resp.AggregationIntervalSeconds)
	}
	if resp.MaxConcurrentProfilers != 5 {
		t.Errorf("MaxConcurrentProfilers = %d, want 5", resp.MaxConcurrentProfilers)
	}
}

func TestCapabilitiesReturnsIndependentMemoryModeMap(t *testing.T) {
	h := &Handler{}
	resp := buildCapabilities(h)
	resp.MemoryModes["new"] = []string{"new_mode"}
	resp.MemoryModes["go"][0] = "modified"

	next := buildCapabilities(h)
	if next.MemoryModes["go"][0] != "physical_alloc" {
		t.Errorf("MemoryModes was mutated across responses")
	}
	if _, ok := next.MemoryModes["new"]; ok {
		t.Errorf("MemoryModes retained a caller mutation")
	}
}

func TestProfilingPrivateDataUsesRequestJSONNames(t *testing.T) {
	data, err := newProfilingPrivateData(&v1.CreateProfilingJobRequest{
		BinaryMatchPath: "/usr/bin/example",
		DurationSeconds: 60,
		Language:        "go",
		MemoryMode:      "object_alloc",
	})
	if err != nil {
		t.Fatalf("newProfilingPrivateData() error=%v", err)
	}

	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("json.Unmarshal() error=%v", err)
	}
	if fields["binary_match_path"] != "/usr/bin/example" ||
		fields["duration_seconds"] != float64(60) ||
		fields["language"] != "go" ||
		fields["memory_mode"] != "object_alloc" {
		t.Errorf("newProfilingPrivateData()=%s, want request fields", data)
	}
}

func TestBuildProfilingJobReadsPrivateData(t *testing.T) {
	resp, err := buildProfilingJob(&job.Job{
		Type:   ProfilingMemory,
		Status: job.JobStatusRunning,
		PrivateData: json.RawMessage(`{
			"binary_match_path":"/usr/bin/example",
			"duration_seconds":60,
			"language":"go",
			"memory_mode":"object_alloc"
		}`),
	}, "")
	if err != nil {
		t.Fatalf("buildProfilingJob() error = %v", err)
	}

	if resp.BinaryMatchPath != "/usr/bin/example" {
		t.Errorf("BinaryMatchPath=%q, want %q", resp.BinaryMatchPath, "/usr/bin/example")
	}
	if resp.Language != "go" {
		t.Errorf("Language=%q, want %q", resp.Language, "go")
	}
	if resp.MemoryMode != "object_alloc" {
		t.Errorf("MemoryMode=%q, want %q", resp.MemoryMode, "object_alloc")
	}
	if resp.DurationSeconds != 60 {
		t.Errorf("DurationSeconds=%d, want 60", resp.DurationSeconds)
	}
}

func TestBuildProfilingJobRejectsNonProfilingJob(t *testing.T) {
	_, err := buildProfilingJob(&job.Job{Type: "trace"}, "")
	if err == nil {
		t.Fatal("buildProfilingJob() error = nil, want non-nil")
	}
}

func TestBuildProfilingJobRejectsInvalidPrivateData(t *testing.T) {
	_, err := buildProfilingJob(&job.Job{
		Type:        ProfilingCPU,
		PrivateData: json.RawMessage(`{"duration_seconds":`),
	}, "")
	if err == nil {
		t.Fatal("buildProfilingJob() error = nil, want non-nil")
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

func TestBuildProfilingJobBuildsResultWithoutMutatingJob(t *testing.T) {
	jobResult := &job.Job{
		ID:           "profile-2026",
		Type:         ProfilingCPU,
		Hostname:     "huatuo-dev",
		Status:       job.JobStatusCompleted,
		CreatedAt:    time.Date(2026, time.July, 20, 10, 0, 0, 0, time.UTC),
		FinishedAt:   time.Date(2026, time.July, 20, 10, 1, 0, 0, time.UTC),
		ErrorMessage: "collection completed with partial symbols",
		PrivateData:  json.RawMessage(`{"duration_seconds":60}`),
	}

	resp, err := buildProfilingJob(jobResult, "http://grafana.example/d")
	if err != nil {
		t.Fatalf("buildProfilingJob() error = %v", err)
	}
	if resp.ResultURL == nil || *resp.ResultURL == "" {
		t.Error("buildProfilingJob() result URL is empty")
	}
	if jobResult.Result.URL != "" {
		t.Errorf("job result URL mutated to %q", jobResult.Result.URL)
	}
	if resp.DurationSeconds != 60 {
		t.Errorf("DurationSeconds=%d, want 60", resp.DurationSeconds)
	}
	if !resp.CreatedAt.Equal(jobResult.CreatedAt) {
		t.Errorf("CreatedAt=%s, want %s", resp.CreatedAt, jobResult.CreatedAt)
	}
	if resp.FinishedAt == nil || !resp.FinishedAt.Equal(jobResult.FinishedAt) {
		t.Errorf("FinishedAt=%v, want %s", resp.FinishedAt, jobResult.FinishedAt)
	}
	if resp.StatusReason == nil || *resp.StatusReason != jobResult.ErrorMessage {
		t.Errorf("StatusReason=%v, want %q", resp.StatusReason, jobResult.ErrorMessage)
	}
}

func TestBuildProfilingJobLeavesUnavailableFieldsNull(t *testing.T) {
	resp, err := buildProfilingJob(&job.Job{Type: ProfilingCPU}, "")
	if err != nil {
		t.Fatalf("buildProfilingJob() error = %v", err)
	}
	if resp.FinishedAt != nil {
		t.Errorf("FinishedAt=%v, want nil", resp.FinishedAt)
	}
	if resp.ResultURL != nil {
		t.Errorf("ResultURL=%v, want nil", resp.ResultURL)
	}
	if resp.StatusReason != nil {
		t.Errorf("StatusReason=%v, want nil", resp.StatusReason)
	}
}

func TestBuildProfilingJobOmitsResultURLWithoutDashboard(t *testing.T) {
	resp, err := buildProfilingJob(&job.Job{
		Type:   ProfilingCPU,
		Status: job.JobStatusCompleted,
	}, "")
	if err != nil {
		t.Fatalf("buildProfilingJob() error = %v", err)
	}
	if resp.ResultURL != nil {
		t.Errorf("ResultURL=%v, want nil", resp.ResultURL)
	}
}

func TestRawProfilesMapsStableWireType(t *testing.T) {
	uploadedAt := time.Date(2026, 7, 22, 10, 0, 1, 0, time.UTC)
	document := &profileService.ProfileDocument{
		Hostname:     "node-a",
		UploadedTime: uploadedAt,
		TracerID:     "internal-trace-id",
		TracerTime:   "2026-07-22 10:00:00.000 +0000",
	}
	document.TracerData.Flamedata.ProfileType = "process_cpu"

	items, err := rawProfiles([]*profileService.ProfileDocument{nil, document})
	if err != nil {
		t.Fatalf("rawProfiles() error=%v", err)
	}
	if len(items) != 1 {
		t.Fatalf("rawProfiles() length = %d, want 1", len(items))
	}
	if items[0].Hostname != document.Hostname {
		t.Fatalf("Hostname=%q, want %q", items[0].Hostname, document.Hostname)
	}
	if got := items[0].ProfileType; got != "process_cpu" {
		t.Fatalf("profile type = %q, want process_cpu", got)
	}
	wantCapturedAt := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	if !items[0].CapturedAt.Equal(wantCapturedAt) {
		t.Fatalf("CapturedAt=%s, want %s", items[0].CapturedAt, wantCapturedAt)
	}
	if len(items[0].Profile) == 0 {
		t.Fatal("Profile is empty")
	}
}
