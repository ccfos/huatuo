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
		Type:      ProfilingCPU,
		Container: "container+2026&debug",
		StartTime: time.Date(2026, 6, 24, 10, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 6, 24, 10, 5, 0, 0, time.UTC),
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

	if len(resp.ProfileTypes) != 2 {
		t.Errorf("ProfileTypes len = %d, want 2", len(resp.ProfileTypes))
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

	if resp.DefaultAggregationInterval != 15 {
		t.Errorf("DefaultAggregationInterval = %d, want 15", resp.DefaultAggregationInterval)
	}
	if resp.DefaultExecutionTimeout != 30 {
		t.Errorf("DefaultExecutionTimeout = %d, want 30", resp.DefaultExecutionTimeout)
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
			req := &job.NewAgentTaskReq{}
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
		Language:        "go",
		MemoryMode:      "object_alloc",
	})

	want := map[string]any{
		"binary_match_path": "/usr/bin/example",
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
	h := &Handler{}
	resp := h.convertJobToProfilingResponse(&job.Job{
		Status: job.JobStatusRunning,
		PrivateData: map[string]any{
			"binary_match_path": "/usr/bin/example",
			"language":          "go",
			"memory_mode":       "object_alloc",
		},
	})

	if resp.TargetExecPath != "/usr/bin/example" {
		t.Errorf("TargetExecPath=%q, want %q", resp.TargetExecPath, "/usr/bin/example")
	}
	if resp.TargetProcessLanguage != "go" {
		t.Errorf("TargetProcessLanguage=%q, want %q", resp.TargetProcessLanguage, "go")
	}
	if resp.MemoryMode != "object_alloc" {
		t.Errorf("MemoryMode=%q, want %q", resp.MemoryMode, "object_alloc")
	}
}

func TestValidateProfilingJobListQuery(t *testing.T) {
	tests := []struct {
		name      string
		query     profilingJobListQuery
		wantTypes []string
		wantErr   string
	}{
		{
			name: "all profile types",
			query: profilingJobListQuery{
				ContainerID: "container-2026",
				Hostname:    "huatuo-dev",
				Status:      string(job.JobStatusRunning),
			},
			wantTypes: []string{ProfilingMemory, ProfilingCPU},
		},
		{
			name:      "cpu profile type",
			query:     profilingJobListQuery{Type: "cpu"},
			wantTypes: []string{ProfilingCPU},
		},
		{
			name:    "invalid profile type",
			query:   profilingJobListQuery{Type: "offcpu"},
			wantErr: `invalid type "offcpu"`,
		},
		{
			name:    "invalid job status",
			query:   profilingJobListQuery{Status: "unknown"},
			wantErr: `invalid status "unknown"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queries, err := validateProfilingJobListQuery(tt.query)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Errorf("validateProfilingJobListQuery() error=%v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateProfilingJobListQuery() error=%v, want nil", err)
			}
			if len(queries) != len(tt.wantTypes) {
				t.Fatalf("validateProfilingJobListQuery() len=%d, want %d", len(queries), len(tt.wantTypes))
			}
			for i, wantType := range tt.wantTypes {
				if queries[i].Type != wantType {
					t.Errorf("validateProfilingJobListQuery()[%d].Type=%q, want %q", i, queries[i].Type, wantType)
				}
				if queries[i].ContainerID != tt.query.ContainerID {
					t.Errorf("validateProfilingJobListQuery()[%d].ContainerID=%q, want %q", i, queries[i].ContainerID, tt.query.ContainerID)
				}
				if queries[i].Hostname != tt.query.Hostname {
					t.Errorf("validateProfilingJobListQuery()[%d].Hostname=%q, want %q", i, queries[i].Hostname, tt.query.Hostname)
				}
			}
		})
	}
}
