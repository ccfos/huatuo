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
	profiletypes "huatuo-bamai/pkg/profiling"
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
			name: "native CPU profiling by thread group and CPU IDs",
			req: v1.CreateProfilingJobRequest{
				ProfilingType: "cpu",
				Language:      "go",
				Duration:      30,
				Hostname:      "huatuo-dev",
				CPUIDs:        []int{3, 1},
				PID:           4242,
				ThreadGroup:   true,
			},
			wantType: ProfilingCPU,
			wantTracerArgs: []string{
				"-t", "cpu",
				"-l", "go",
				"--pid", "4242",
				"--thread-group",
				"--cpuid", "1,3",
				"--duration", "30",
				"--aggr-interval", "10",
				"--max-concurrent-procs", "2",
				"--output-format", "remote",
				"--output-storage", "/var/run/huatuo-toolstream.sock",
			},
		},
		{
			name: "native CPU profiling by container",
			req: v1.CreateProfilingJobRequest{
				ProfilingType: "cpu",
				Language:      "c",
				Duration:      30,
				ContainerID:   "0123456789ab",
				Hostname:      "huatuo-dev",
			},
			wantType: ProfilingCPU,
			wantTracerArgs: []string{
				"-t", "cpu",
				"-l", "c",
				"--container-id", "0123456789ab",
				"--duration", "30",
				"--aggr-interval", "10",
				"--max-concurrent-procs", "2",
				"--output-format", "remote",
				"--output-storage", "/var/run/huatuo-toolstream.sock",
			},
		},
		{
			name: "java profiling with external tool",
			req: v1.CreateProfilingJobRequest{
				ProfilingType: "cpu",
				Language:      "java",
				ToolPath:      "/opt/async-profiler",
				Duration:      30,
			},
			wantType: ProfilingCPU,
			wantTracerArgs: []string{
				"-t", "cpu",
				"-l", "java",
				"--tool-path", "/opt/async-profiler",
				"--duration", "30",
				"--aggr-interval", "10",
				"--max-concurrent-procs", "2",
				"--output-format", "remote",
				"--output-storage", "/var/run/huatuo-toolstream.sock",
			},
		},
		{
			name: "native memory profiling by exact PID",
			req: v1.CreateProfilingJobRequest{
				ProfilingType: "memory",
				Language:      "c",
				MemoryMode:    "physical_usage",
				Duration:      30,
				PID:           4242,
			},
			wantType: ProfilingMemory,
			wantTracerArgs: []string{
				"-t", "memory",
				"--memory-mode", "physical_usage",
				"-l", "c",
				"--pid", "4242",
				"--duration", "30",
				"--aggr-interval", "10",
				"--max-concurrent-procs", "2",
				"--output-format", "remote",
				"--output-storage", "/var/run/huatuo-toolstream.sock",
			},
		},
		{
			name: "java profiling requires external tool",
			req: v1.CreateProfilingJobRequest{
				ProfilingType: "cpu",
				Language:      "java",
				ToolPath:      "   ",
				Duration:      30,
			},
			wantErr: `language "java" requires tool_path`,
		},
		{
			name: "mutex wait profiling by PID",
			req: v1.CreateProfilingJobRequest{
				ProfilingType:     "lock",
				Language:          "go",
				Duration:          30,
				PID:               4242,
				LockMode:          profiletypes.LockModeWaitTime,
				LockType:          profiletypes.LockTypeMutex,
				LockWaitThreshold: "10us",
			},
			wantType: ProfilingLock,
			wantTracerArgs: []string{
				"-t", "lock",
				"-l", "go",
				"--lock-type", "mutex",
				"--lock-wait-threshold", "10us",
				"--pid", "4242",
				"--duration", "30",
				"--aggr-interval", "10",
				"--max-concurrent-procs", "2",
				"--output-format", "remote",
				"--output-storage", "/var/run/huatuo-toolstream.sock",
			},
		},
		{
			name: "native PID and container are mutually exclusive",
			req: v1.CreateProfilingJobRequest{
				ProfilingType: "cpu",
				Language:      "go",
				Duration:      30,
				ContainerID:   "0123456789ab",
				PID:           4242,
			},
			wantErr: "pid and container_id are mutually exclusive",
		},
		{
			name: "thread group requires PID",
			req: v1.CreateProfilingJobRequest{
				ProfilingType: "cpu",
				Language:      "go",
				Duration:      30,
				ThreadGroup:   true,
			},
			wantErr: "thread_group requires pid",
		},
		{
			name: "CPU IDs require CPU profiling",
			req: v1.CreateProfilingJobRequest{
				ProfilingType: "memory",
				Language:      "go",
				MemoryMode:    "virtual_alloc",
				Duration:      30,
				PID:           4242,
				CPUIDs:        []int{1},
			},
			wantErr: "cpu_ids are supported only by CPU profiling",
		},
		{
			name: "CPU IDs reject duplicates",
			req: v1.CreateProfilingJobRequest{
				ProfilingType: "cpu",
				Language:      "go",
				Duration:      30,
				CPUIDs:        []int{1, 1},
			},
			wantErr: "duplicate cpu_id 1",
		},
		{
			name: "non-native profiling rejects native selection",
			req: v1.CreateProfilingJobRequest{
				ProfilingType: "cpu",
				Language:      "java",
				Duration:      30,
				PID:           4242,
			},
			wantErr: "pid, cpu_ids, and thread_group are supported only by native profiling",
		},
		{
			name: "mutex wait profiling by container uses defaults",
			req: v1.CreateProfilingJobRequest{
				ProfilingType: "lock",
				Language:      "c++",
				Duration:      30,
				ContainerID:   "0123456789abcdef",
			},
			wantType: ProfilingLock,
			wantTracerArgs: []string{
				"-t", "lock",
				"-l", "c++",
				"--lock-type", "mutex",
				"--lock-wait-threshold", "1us",
				"--container-id", "0123456789abcdef",
				"--duration", "30",
				"--aggr-interval", "10",
				"--max-concurrent-procs", "2",
				"--output-format", "remote",
				"--output-storage", "/var/run/huatuo-toolstream.sock",
			},
		},
		{
			name: "lock profiling rejects host-wide target",
			req: v1.CreateProfilingJobRequest{
				ProfilingType: "lock",
				Language:      "c",
				Duration:      30,
			},
			wantErr: "lock profiling requires exactly one of pid or container_id",
		},
		{
			name: "lock profiling rejects multiple targets",
			req: v1.CreateProfilingJobRequest{
				ProfilingType: "lock",
				Language:      "c",
				Duration:      30,
				PID:           4242,
				ContainerID:   "0123456789abcdef",
			},
			wantErr: "lock profiling requires exactly one of pid or container_id",
		},
		{
			name: "lock profiling rejects non-native language",
			req: v1.CreateProfilingJobRequest{
				ProfilingType: "lock",
				Language:      "java",
				Duration:      30,
				PID:           4242,
			},
			wantErr: `lock profiling not supported for "java"`,
		},
		{
			name: "lock profiling rejects negative PID",
			req: v1.CreateProfilingJobRequest{
				ProfilingType: "lock",
				Language:      "go",
				Duration:      30,
				PID:           -1,
			},
			wantErr: "pid must be between 1 and 2147483647",
		},
		{
			name: "lock profiling rejects invalid threshold",
			req: v1.CreateProfilingJobRequest{
				ProfilingType:     "lock",
				Language:          "go",
				Duration:          30,
				PID:               4242,
				LockWaitThreshold: "later",
			},
			wantErr: `invalid lock_wait_threshold "later": time: invalid duration "later"`,
		},
		{
			name: "lock profiling rejects unsupported mode",
			req: v1.CreateProfilingJobRequest{
				ProfilingType: "lock",
				Language:      "go",
				Duration:      30,
				PID:           4242,
				LockMode:      profiletypes.LockMode("count"),
			},
			wantErr: `unsupported lock mode "count"`,
		},
		{
			name: "lock profiling rejects unsupported lock type",
			req: v1.CreateProfilingJobRequest{
				ProfilingType: "lock",
				Language:      "go",
				Duration:      30,
				PID:           4242,
				LockType:      profiletypes.LockType("semaphore"),
			},
			wantErr: `unsupported lock type "semaphore" ` +
				`(expected: mutex, spinlock, or rwlock)`,
		},
		{
			name: "rwlock profiling accepts thread group target",
			req: v1.CreateProfilingJobRequest{
				ProfilingType: "lock",
				Language:      "go",
				Duration:      30,
				PID:           4242,
				ThreadGroup:   true,
				LockType:      profiletypes.LockTypeRWLock,
			},
			wantType: ProfilingLock,
			wantTracerArgs: []string{
				"-t", "lock",
				"-l", "go",
				"--lock-type", "rwlock",
				"--lock-wait-threshold", "1us",
				"--pid", "4242",
				"--thread-group",
				"--duration", "30",
				"--aggr-interval", "10",
				"--max-concurrent-procs", "2",
				"--output-format", "remote",
				"--output-storage", "/var/run/huatuo-toolstream.sock",
			},
		},
		{
			name: "spinlock profiling by PID",
			req: v1.CreateProfilingJobRequest{
				ProfilingType: "lock",
				Language:      "c",
				Duration:      30,
				PID:           4242,
				LockType:      profiletypes.LockTypeSpinlock,
			},
			wantType: ProfilingLock,
			wantTracerArgs: []string{
				"-t", "lock",
				"-l", "c",
				"--lock-type", "spinlock",
				"--lock-wait-threshold", "1us",
				"--pid", "4242",
				"--duration", "30",
				"--aggr-interval", "10",
				"--max-concurrent-procs", "2",
				"--output-format", "remote",
				"--output-storage", "/var/run/huatuo-toolstream.sock",
			},
		},
		{
			name: "native memory requires a target",
			req: v1.CreateProfilingJobRequest{
				ProfilingType: "memory",
				Language:      "go",
				MemoryMode:    "virtual_alloc",
				Duration:      30,
			},
			wantErr: "native memory profiling requires pid or container_id",
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

	cfg := Config{
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
			if !slices.Equal(got.AgentTask.TracerArgs, tt.wantTracerArgs) {
				t.Errorf("TracerArgs=%q, want %q", got.AgentTask.TracerArgs, tt.wantTracerArgs)
			}
			var privateData profilingJobPrivateData
			if err := json.Unmarshal(got.PrivateData, &privateData); err != nil {
				t.Fatalf("json.Unmarshal(PrivateData) error=%v", err)
			}
			if !slices.Equal(privateData.CPUIDs, tt.req.CPUIDs) ||
				privateData.PID != tt.req.PID ||
				privateData.ThreadGroup != tt.req.ThreadGroup {
				t.Errorf("PrivateData=%+v, want native target from request", privateData)
			}
		})
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
			wantTypes: []job.JobType{ProfilingMemory, ProfilingCPU, ProfilingLock},
		},
		{name: "cpu profiling", query: profilingJobListQuery{Type: "cpu"}, wantTypes: []job.JobType{ProfilingCPU}},
		{name: "memory profiling", query: profilingJobListQuery{Type: "memory"}, wantTypes: []job.JobType{ProfilingMemory}},
		{name: "lock profiling", query: profilingJobListQuery{Type: "lock"}, wantTypes: []job.JobType{ProfilingLock}},
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
