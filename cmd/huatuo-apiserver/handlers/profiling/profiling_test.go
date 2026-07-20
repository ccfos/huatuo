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

	err := fillMemoryTracerArgs(req, "", "NATIVE_PHYSICAL_USAGE")
	if err != nil {
		t.Fatalf("fillMemoryTracerArgs() error = %v", err)
	}

	want := []string{"-t", "memory", "--memory-mode", "physical_usage", "-l", "c"}
	if strings.Join(req.TracerArgs, " ") != strings.Join(want, " ") {
		t.Fatalf("TracerArgs = %q, want %q", req.TracerArgs, want)
	}
}

func TestFillTracerArgsQuotesUnsupportedValues(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*job.NewAgentTaskReq) error
		want      string
	}{
		{
			name: "cpu language",
			configure: func(req *job.NewAgentTaskReq) error {
				return fillCPUTracerArgs(req, "", "unknown language")
			},
			want: `cpu profiling not supported for "unknown language"`,
		},
		{
			name: "memory language",
			configure: func(req *job.NewAgentTaskReq) error {
				return fillMemoryTracerArgs(req, "unknown language", "physical_alloc")
			},
			want: `memory profiling not supported for "unknown language"`,
		},
		{
			name: "memory mode",
			configure: func(req *job.NewAgentTaskReq) error {
				return fillMemoryTracerArgs(req, "c", "unknown mode")
			},
			want: `memory mode not supported: "unknown mode"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.configure(&job.NewAgentTaskReq{})
			if err == nil {
				t.Fatal("configure() error = nil, want non-nil")
			}
			if err.Error() != tt.want {
				t.Fatalf("configure() error = %q, want %q", err, tt.want)
			}
		})
	}
}
