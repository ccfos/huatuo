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
	"reflect"
	"testing"

	v1 "huatuo-bamai/apis/v1"
	"huatuo-bamai/cmd/huatuo-apiserver/config"
	"huatuo-bamai/internal/job"
)

func TestBuildCreateProfilingJobRequest(t *testing.T) {
	tests := []struct {
		name           string
		req            v1.CreateProfilingJobRequest
		wantType       string
		wantTracerArgs []string
		wantErr        string
	}{
		{
			name: "cpu profiling",
			req: v1.CreateProfilingJobRequest{
				ProfilingType:   "cpu",
				Language:        "go",
				BinaryMatchPath: "/usr/bin/example",
				Duration:        30,
				ContainerID:     "container-2026",
				Hostname:        "huatuo-dev",
			},
			wantType: ProfilingCPU,
			wantTracerArgs: []string{
				"-t", "cpu",
				"--binary-match-path", "/usr/bin/example",
				"-l", "go",
				"--duration", "30",
				"--aggr-interval", "10",
				"--max-concurrent-procs", "2",
				"--output-format", "remote",
				"--output-storage", "/var/run/huatuo-toolstream.sock",
			},
		},
		{
			name: "memory profiling",
			req: v1.CreateProfilingJobRequest{
				ProfilingType: "memory",
				Language:      "c",
				MemoryMode:    "physical_usage",
				Duration:      30,
			},
			wantType: ProfilingMemory,
			wantTracerArgs: []string{
				"-t", "memory",
				"--memory-mode", "physical_usage",
				"-l", "c",
				"--duration", "30",
				"--aggr-interval", "10",
				"--max-concurrent-procs", "2",
				"--output-format", "remote",
				"--output-storage", "/var/run/huatuo-toolstream.sock",
			},
		},
		{
			name: "unsupported type",
			req: v1.CreateProfilingJobRequest{
				ProfilingType: "offcpu",
				Duration:      30,
			},
			wantErr: `unsupported profiling type "offcpu"`,
		},
		{
			name: "duration below two intervals",
			req: v1.CreateProfilingJobRequest{
				ProfilingType: "cpu",
				Language:      "go",
				Duration:      19,
			},
			wantErr: "duration must cover at least two profiling intervals",
		},
	}

	cfg := config.ProfilingConfig{
		AggregationInterval: 10,
		ExecutionTimeout:    20,
		MaxProfilerProcs:    2,
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildCreateProfilingJobRequest(&tt.req, "operator-2026", &cfg)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("buildCreateProfilingJobRequest() error=%v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildCreateProfilingJobRequest() error=%v", err)
			}
			if got.Type != tt.wantType {
				t.Errorf("Type=%q, want %q", got.Type, tt.wantType)
			}
			if got.UserID != "operator-2026" {
				t.Errorf("UserID=%q, want operator-2026", got.UserID)
			}
			if got.AgentTask.Duration != tt.req.Duration*2 {
				t.Errorf("AgentTask.Duration=%d, want %d", got.AgentTask.Duration, tt.req.Duration*2)
			}
			if !reflect.DeepEqual(got.AgentTask.TracerArgs, tt.wantTracerArgs) {
				t.Errorf("TracerArgs=%q, want %q", got.AgentTask.TracerArgs, tt.wantTracerArgs)
			}
		})
	}
}

func TestBuildProfilingJobQueries(t *testing.T) {
	tests := []struct {
		name      string
		query     profilingJobListQuery
		wantTypes []string
		wantErr   string
	}{
		{
			name: "all profiling types",
			query: profilingJobListQuery{
				ContainerID: "container-2026",
				Hostname:    "huatuo-dev",
				Status:      string(job.JobStatusRunning),
			},
			wantTypes: []string{ProfilingMemory, ProfilingCPU},
		},
		{name: "cpu profiling", query: profilingJobListQuery{Type: "cpu"}, wantTypes: []string{ProfilingCPU}},
		{name: "memory profiling", query: profilingJobListQuery{Type: "memory"}, wantTypes: []string{ProfilingMemory}},
		{name: "invalid type", query: profilingJobListQuery{Type: "offcpu"}, wantErr: `invalid type "offcpu"`},
		{name: "invalid status", query: profilingJobListQuery{Status: "unknown"}, wantErr: `invalid status "unknown"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildProfilingJobQueries(tt.query)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("buildProfilingJobQueries() error=%v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildProfilingJobQueries() error=%v", err)
			}
			if len(got) != len(tt.wantTypes) {
				t.Fatalf("buildProfilingJobQueries() len=%d, want %d", len(got), len(tt.wantTypes))
			}
			for i, wantType := range tt.wantTypes {
				if got[i].Type != wantType {
					t.Errorf("JobQueries[%d].Type=%q, want %q", i, got[i].Type, wantType)
				}
				if got[i].ContainerID != tt.query.ContainerID || got[i].Hostname != tt.query.Hostname {
					t.Errorf("JobQueries[%d]=%+v, want request target fields", i, got[i])
				}
			}
		})
	}
}

func TestValidateProfilingJobID(t *testing.T) {
	if _, err := validateProfilingJobID(""); err == nil || err.Error() != "id is required" {
		t.Fatalf("validateProfilingJobID() error=%v, want id is required", err)
	}

	got, err := validateProfilingJobID("profile-2026")
	if err != nil {
		t.Fatalf("validateProfilingJobID() error=%v", err)
	}
	if got != "profile-2026" {
		t.Errorf("validateProfilingJobID()=%q, want profile-2026", got)
	}
}
