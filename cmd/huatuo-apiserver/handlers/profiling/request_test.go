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
	"testing"

	v1 "huatuo-bamai/apis/v1"
	"huatuo-bamai/internal/job"
)

func TestBuildCreateProfilingJobRequest(t *testing.T) {
	tests := []struct {
		name           string
		req            v1.CreateProfilingJobRequest
		wantType       job.JobType
		wantTracerArgs []string
		wantErr        string
	}{
		{
			name: "native CPU container and CPU selection",
			req: v1.CreateProfilingJobRequest{
				ProfilingType:   "cpu",
				Language:        "go",
				DurationSeconds: 30,
				ContainerID:     "container-2026",
				Hostname:        "huatuo-dev",
				CPUIDs:          []int{3, 1},
			},
			wantType: ProfilingCPU,
			wantTracerArgs: []string{
				"-t", "cpu",
				"-l", "go",
				"--container-id", "container-2026",
				"--cpuid", "1,3",
				"--duration", "30",
				"--aggr-interval", "10",
				"--max-concurrent-procs", "2",
				"--output-format", "remote",
				"--output-storage", "/var/run/huatuo-toolstream.sock",
			},
		},
		{
			name: "native memory PID",
			req: v1.CreateProfilingJobRequest{
				ProfilingType:   "memory",
				Language:        "c",
				MemoryMode:      "physical_usage",
				DurationSeconds: 30,
				PID:             4321,
			},
			wantType: ProfilingMemory,
			wantTracerArgs: []string{
				"-t", "memory",
				"--memory-mode", "physical_usage",
				"-l", "c",
				"--pid", "4321",
				"--duration", "30",
				"--aggr-interval", "10",
				"--max-concurrent-procs", "2",
				"--output-format", "remote",
				"--output-storage", "/var/run/huatuo-toolstream.sock",
			},
		},
		{
			name: "native CPU thread group",
			req: v1.CreateProfilingJobRequest{
				ProfilingType:   "cpu",
				Language:        "go",
				DurationSeconds: 30,
				PID:             4321,
				ThreadGroup:     true,
				CPUIDs:          []int{4, 1},
			},
			wantType: ProfilingCPU,
			wantTracerArgs: []string{
				"-t", "cpu",
				"-l", "go",
				"--pid", "4321",
				"--thread-group",
				"--cpuid", "1,4",
				"--duration", "30",
				"--aggr-interval", "10",
				"--max-concurrent-procs", "2",
				"--output-format", "remote",
				"--output-storage", "/var/run/huatuo-toolstream.sock",
			},
		},
		{
			name: "external container target",
			req: v1.CreateProfilingJobRequest{
				ProfilingType:   "cpu",
				Language:        "java",
				BinaryMatchPath: "/usr/bin/java",
				DurationSeconds: 30,
				ContainerID:     "container-2026",
			},
			wantType: ProfilingCPU,
			wantTracerArgs: []string{
				"-t", "cpu",
				"--binary-match-path", "/usr/bin/java",
				"-l", "java",
				"--container-id", "container-2026",
				"--duration", "30",
				"--aggr-interval", "10",
				"--max-concurrent-procs", "2",
				"--output-format", "remote",
				"--output-storage", "/var/run/huatuo-toolstream.sock",
			},
		},
		{
			name: "external PID target",
			req: v1.CreateProfilingJobRequest{
				ProfilingType:   "cpu",
				Language:        "java",
				BinaryMatchPath: "/usr/bin/java",
				DurationSeconds: 30,
				PID:             4321,
			},
			wantType: ProfilingCPU,
			wantTracerArgs: []string{
				"-t", "cpu",
				"--binary-match-path", "/usr/bin/java",
				"-l", "java",
				"--pid", "4321",
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
				ProfilingType:   "offcpu",
				DurationSeconds: 30,
			},
			wantErr: `unsupported profiling type "offcpu"`,
		},
		{
			name: "duration below two intervals",
			req: v1.CreateProfilingJobRequest{
				ProfilingType:   "cpu",
				Language:        "go",
				DurationSeconds: 19,
			},
			wantErr: "duration_seconds must cover at least two profiling intervals",
		},
		{
			name: "native memory target required",
			req: v1.CreateProfilingJobRequest{
				ProfilingType:   "memory",
				Language:        "c",
				MemoryMode:      "physical_usage",
				DurationSeconds: 30,
			},
			wantErr: "exactly one of pid or container_id must be provided",
		},
		{
			name: "PID and container conflict",
			req: v1.CreateProfilingJobRequest{
				ProfilingType:   "cpu",
				Language:        "go",
				DurationSeconds: 30,
				PID:             4321,
				ContainerID:     "container-2026",
			},
			wantErr: "pid and container_id are mutually exclusive",
		},
		{
			name: "thread group requires PID",
			req: v1.CreateProfilingJobRequest{
				ProfilingType:   "cpu",
				Language:        "go",
				DurationSeconds: 30,
				ThreadGroup:     true,
			},
			wantErr: "thread_group requires pid",
		},
		{
			name: "thread group requires native profiler",
			req: v1.CreateProfilingJobRequest{
				ProfilingType:   "cpu",
				Language:        "java",
				DurationSeconds: 30,
				PID:             4321,
				ThreadGroup:     true,
			},
			wantErr: "cpu_ids and thread_group are supported only by native profiling",
		},
		{
			name: "external profiler target required",
			req: v1.CreateProfilingJobRequest{
				ProfilingType:   "cpu",
				Language:        "java",
				DurationSeconds: 30,
			},
			wantErr: "exactly one of pid or container_id must be provided",
		},
		{
			name: "CPU IDs require native CPU profiler",
			req: v1.CreateProfilingJobRequest{
				ProfilingType:   "memory",
				Language:        "c",
				MemoryMode:      "physical_usage",
				DurationSeconds: 30,
				PID:             4321,
				CPUIDs:          []int{1},
			},
			wantErr: "cpu_ids are supported only by native CPU profiling",
		},
		{
			name: "negative CPU ID",
			req: v1.CreateProfilingJobRequest{
				ProfilingType:   "cpu",
				Language:        "go",
				DurationSeconds: 30,
				CPUIDs:          []int{-1},
			},
			wantErr: "cpu_id must not be negative: -1",
		},
		{
			name: "duplicate CPU ID",
			req: v1.CreateProfilingJobRequest{
				ProfilingType:   "cpu",
				Language:        "go",
				DurationSeconds: 30,
				CPUIDs:          []int{1, 1},
			},
			wantErr: "duplicate cpu_id 1",
		},
		{
			name: "invalid PID",
			req: v1.CreateProfilingJobRequest{
				ProfilingType:   "cpu",
				Language:        "go",
				DurationSeconds: 30,
				PID:             -1,
			},
			wantErr: "pid must be between 1 and 2147483647",
		},
	}

	cfg := Config{
		AggregationIntervalSeconds:     10,
		MaxConcurrentProfilerProcesses: 2,
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
			if got.AgentTask.Duration != tt.req.DurationSeconds*2 {
				t.Errorf(
					"AgentTask.Duration=%d, want %d",
					got.AgentTask.Duration,
					tt.req.DurationSeconds*2,
				)
			}
			wantTraceTimeout := tt.req.DurationSeconds + cfg.AggregationIntervalSeconds
			if got.AgentTask.TraceTimeout != wantTraceTimeout {
				t.Errorf(
					"AgentTask.TraceTimeout=%d, want %d",
					got.AgentTask.TraceTimeout,
					wantTraceTimeout,
				)
			}
			if !slices.Equal(got.AgentTask.TracerArgs, tt.wantTracerArgs) {
				t.Errorf("TracerArgs=%q, want %q", got.AgentTask.TracerArgs, tt.wantTracerArgs)
			}
		})
	}
}

func TestBuildCreateProfilingJobRequestDoesNotMutateInput(t *testing.T) {
	req := v1.CreateProfilingJobRequest{
		ProfilingType:   "cpu",
		Language:        "go",
		DurationSeconds: 30,
		CPUIDs:          []int{3, 1},
	}
	cfg := Config{
		AggregationIntervalSeconds:     10,
		MaxConcurrentProfilerProcesses: 2,
	}

	got, err := buildCreateProfilingJobRequest(&req, "operator-2026", &cfg)
	if err != nil {
		t.Fatalf("buildCreateProfilingJobRequest() error=%v", err)
	}
	if !slices.Equal(req.CPUIDs, []int{3, 1}) {
		t.Fatalf("request CPU IDs mutated to %v", req.CPUIDs)
	}

	var privateData profilingJobPrivateData
	if err := json.Unmarshal(got.PrivateData, &privateData); err != nil {
		t.Fatalf("json.Unmarshal() error=%v", err)
	}
	if !slices.Equal(privateData.CPUIDs, []int{1, 3}) {
		t.Errorf("private CPU IDs=%v, want [1 3]", privateData.CPUIDs)
	}
}

func TestBuildProfilingJobQueries(t *testing.T) {
	tests := []struct {
		name      string
		query     profilingJobListQuery
		wantTypes []job.JobType
		wantErr   string
	}{
		{
			name: "all profiling types",
			query: profilingJobListQuery{
				ContainerID: "container-2026",
				Hostname:    "huatuo-dev",
				Status:      string(job.JobStatusRunning),
			},
			wantTypes: []job.JobType{ProfilingMemory, ProfilingCPU},
		},
		{name: "cpu profiling", query: profilingJobListQuery{Type: "cpu"}, wantTypes: []job.JobType{ProfilingCPU}},
		{name: "memory profiling", query: profilingJobListQuery{Type: "memory"}, wantTypes: []job.JobType{ProfilingMemory}},
		{name: "invalid type", query: profilingJobListQuery{Type: "offcpu"}, wantErr: `invalid type "offcpu"`},
		{name: "invalid status", query: profilingJobListQuery{Status: "unknown"}, wantErr: `invalid status "unknown"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildProfilingJobQuery(tt.query)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("buildProfilingJobQuery() error=%v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildProfilingJobQuery() error=%v", err)
			}
			if len(got.Types) != len(tt.wantTypes) {
				t.Fatalf("buildProfilingJobQuery() types=%d, want %d", len(got.Types), len(tt.wantTypes))
			}
			for i, wantType := range tt.wantTypes {
				if got.Types[i] != wantType {
					t.Errorf("Types[%d]=%q, want %q", i, got.Types[i], wantType)
				}
			}
			if got.ContainerID != tt.query.ContainerID || got.Hostname != tt.query.Hostname {
				t.Errorf("JobQuery=%+v, want request target fields", got)
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
