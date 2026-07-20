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
	"strings"
	"testing"
	"time"

	v1 "huatuo-bamai/apis/v1"
	"huatuo-bamai/cmd/huatuo-apiserver/config"
	"huatuo-bamai/internal/job"
	profiledef "huatuo-bamai/pkg/profiling"
)

func TestGetFlameGraphURLEscapesLabelValue(t *testing.T) {
	url := getFlameGraphURL("http://grafana.example/d", &job.Job{
		Type:        ProfilingCPU,
		ContainerID: "container+2026&debug",
		StartTime:   time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC),
		EndTime:     time.Date(2026, 6, 24, 10, 5, 0, 0, time.UTC),
	})

	if !strings.Contains(url, "var-container_hostname=container%2B2026%26debug") {
		t.Fatalf("url = %q, want escaped container label value", url)
	}
}

func TestNewHandlerSnapshotsProfilingConfig(t *testing.T) {
	cfg := config.Get()
	old := cfg.Profiling
	defer func() { cfg.Profiling = old }()

	cfg.Profiling.AggregationInterval = 15
	h := NewHandler(nil)
	cfg.Profiling.AggregationInterval = 30

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
	h := &Handler{profilingConfig: config.ProfilingConfig{
		AggregationInterval: 15,
		ExecutionTimeout:    30,
		MaxProfilerProcs:    5,
	}}
	resp, err := buildCapabilitiesResponse(h)
	if err != nil {
		t.Fatalf("buildCapabilitiesResponse() error = %v", err)
	}

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

func TestFillTracerArgs(t *testing.T) {
	tests := []struct {
		name          string
		profilingType profiledef.Type
		language      profiledef.Language
		typeArgs      []string
		want          []string
	}{
		{
			name:          "cpu binary match path",
			profilingType: profiledef.TypeCPU,
			language:      profiledef.LanguageGo,
			typeArgs:      []string{"--binary-match-path", "/usr/bin/example"},
			want:          []string{"-t", "cpu", "--binary-match-path", "/usr/bin/example", "-l", "go"},
		},
		{
			name:          "memory mode",
			profilingType: profiledef.TypeMemory,
			language:      profiledef.LanguageC,
			typeArgs:      []string{"--memory-mode", "physical_usage"},
			want:          []string{"-t", "memory", "--memory-mode", "physical_usage", "-l", "c"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &job.AgentTaskRequest{}
			fillTracerArgs(req, tt.profilingType, tt.language, tt.typeArgs...)
			if strings.Join(req.TracerArgs, " ") != strings.Join(tt.want, " ") {
				t.Fatalf("TracerArgs = %q, want %q", req.TracerArgs, tt.want)
			}
		})
	}
}

func TestProfilingPrivateDataUsesRequestJSONNames(t *testing.T) {
	privateData := profilingPrivateData(&v1.CreateProfilingJobRequest{
		BinaryMatchPath: "/usr/bin/example",
		Duration:        60,
		Language:        "go",
		MemoryMode:      "object_alloc",
	})

	want := map[string]any{
		"binary_match_path": "/usr/bin/example",
		"duration":          60,
		"language":          "go",
		"memory_mode":       "object_alloc",
	}
	if len(privateData) != len(want) {
		t.Fatalf("profilingPrivateData() len=%d, want %d", len(privateData), len(want))
	}
	for key, wantValue := range want {
		if got := privateData[key]; got != wantValue {
			t.Errorf("profilingPrivateData()[%q]=%v, want %v", key, got, wantValue)
		}
	}
}

func TestConvertJobToProfilingResponseReadsRequestJSONNames(t *testing.T) {
	resp, err := buildProfilingJobResponse(&job.Job{
		Type:   ProfilingMemory,
		Status: job.JobStatusRunning,
		PrivateData: map[string]any{
			"binary_match_path": "/usr/bin/example",
			"duration":          float64(60),
			"language":          "go",
			"memory_mode":       "object_alloc",
		},
	}, "")
	if err != nil {
		t.Fatalf("buildProfilingJobResponse() error = %v", err)
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
	if resp.Duration != 60 {
		t.Errorf("Duration=%d, want 60", resp.Duration)
	}
}

func TestProfilingJobResponseRejectsNonProfilingJob(t *testing.T) {
	_, err := buildProfilingJobResponse(&job.Job{Type: "trace"}, "")
	if err == nil {
		t.Fatal("buildProfilingJobResponse() error = nil, want non-nil")
	}
}

func TestProfilingJobResponseBuildsURLWithoutMutatingJob(t *testing.T) {
	jobResult := &job.Job{
		ID:        "profile-2026",
		Type:      ProfilingCPU,
		Hostname:  "huatuo-dev",
		Status:    job.JobStatusCompleted,
		StartTime: time.Date(2026, time.July, 20, 10, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, time.July, 20, 10, 1, 0, 0, time.UTC),
		PrivateData: map[string]any{
			"duration": 60,
		},
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
