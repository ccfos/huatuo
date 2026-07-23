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

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadValidatesProfilingConfig(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "apiserver.conf")
	contents := []byte(`
[[Auth.users]]
ID = "test-token"
IsAdmin = true

[ElasticSearch]
Address = "http://127.0.0.1:9200"

[Profiling]
AggregationInterval = 10
ExecutionTimeout = 19
MaxProfilerProcs = 10
FlameGraphBaseURL = "http://localhost:8006/d"
`)
	if err := os.WriteFile(configFile, contents, 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	_, err := LoadFile(configFile)
	if err == nil || !strings.Contains(err.Error(), "validating profiling config: execution timeout must be at least 20 seconds") {
		t.Fatalf("Load() error = %v, want profiling validation error", err)
	}
}

func TestLoadFileDoesNotAccumulateMemoryConversion(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "apiserver.conf")
	contents := []byte(`
[[Auth.users]]
ID = "test-token"
IsAdmin = true

[ElasticSearch]
Address = "http://127.0.0.1:9200"

[RuntimeCgroup]
LimitMem = 64

[Profiling]
AggregationInterval = 10
ExecutionTimeout = 20
MaxProfilerProcs = 10
FlameGraphBaseURL = "http://localhost:8006/d"
`)
	if err := os.WriteFile(configFile, contents, 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	first, err := LoadFile(configFile)
	if err != nil {
		t.Fatalf("first LoadFile() error = %v", err)
	}
	second, err := LoadFile(configFile)
	if err != nil {
		t.Fatalf("second LoadFile() error = %v", err)
	}
	want := int64(64 * 1024 * 1024)
	if first.RuntimeCgroup.LimitMem != want || second.RuntimeCgroup.LimitMem != want {
		t.Fatalf(
			"LimitMem = (%d, %d), want (%d, %d)",
			first.RuntimeCgroup.LimitMem,
			second.RuntimeCgroup.LimitMem,
			want,
			want,
		)
	}
}

func TestLoadFileRequiresAuthUserAndElasticsearchAddress(t *testing.T) {
	tests := []struct {
		name      string
		contents  string
		wantError string
	}{
		{
			name: "missing auth user",
			contents: `
[ElasticSearch]
Address = "http://127.0.0.1:9200"
`,
			wantError: "at least one auth user is required",
		},
		{
			name: "missing Elasticsearch address",
			contents: `
[[Auth.users]]
ID = "test-token"
IsAdmin = true
`,
			wantError: "validating Elasticsearch config: address is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configFile := filepath.Join(t.TempDir(), "apiserver.conf")
			if err := os.WriteFile(configFile, []byte(tt.contents), 0o600); err != nil {
				t.Fatalf("os.WriteFile() error = %v", err)
			}
			_, err := LoadFile(configFile)
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("LoadFile() error = %v, want contain %q", err, tt.wantError)
			}
		})
	}
}

func TestAgentConfigValidate(t *testing.T) {
	valid := AgentConfig{
		Port:                      19704,
		RequestTimeoutSeconds:     10,
		StatusRetryAttempts:       3,
		StatusRetryBackoffMillis:  100,
		StatusPollIntervalSeconds: 5,
		MaxConsecutivePollErrors:  3,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	valid.Port = 65536
	if err := valid.Validate(); err == nil || !strings.Contains(err.Error(), "must not exceed 65535") {
		t.Fatalf("Validate() error = %v, want invalid port", err)
	}
}

func TestProfilingConfigValidate(t *testing.T) {
	tests := []struct {
		name      string
		config    ProfilingConfig
		wantError string
	}{
		{
			name: "valid",
			config: ProfilingConfig{
				AggregationInterval: 10,
				ExecutionTimeout:    20,
				MaxProfilerProcs:    10,
				FlameGraphBaseURL:   "http://localhost:8006/d",
			},
		},
		{
			name: "non-positive aggregation interval",
			config: ProfilingConfig{
				ExecutionTimeout:  20,
				FlameGraphBaseURL: "http://localhost:8006/d",
			},
			wantError: "aggregation interval must be greater than 0 seconds",
		},
		{
			name: "aggregation interval leaves no valid duration",
			config: ProfilingConfig{
				AggregationInterval: 1200,
				ExecutionTimeout:    2400,
				FlameGraphBaseURL:   "http://localhost:8006/d",
			},
			wantError: "aggregation interval must be less than 1200 seconds",
		},
		{
			name: "execution timeout too short",
			config: ProfilingConfig{
				AggregationInterval: 10,
				ExecutionTimeout:    19,
				FlameGraphBaseURL:   "http://localhost:8006/d",
			},
			wantError: "execution timeout must be at least 20 seconds",
		},
		{
			name: "negative profiler process limit",
			config: ProfilingConfig{
				AggregationInterval: 10,
				ExecutionTimeout:    20,
				MaxProfilerProcs:    -1,
				FlameGraphBaseURL:   "http://localhost:8006/d",
			},
			wantError: "max profiler procs must not be negative",
		},
		{
			name: "unsupported flame graph URL scheme",
			config: ProfilingConfig{
				AggregationInterval: 10,
				ExecutionTimeout:    20,
				FlameGraphBaseURL:   "ftp://grafana.example/d",
			},
			wantError: "flame graph base url must use http or https",
		},
		{
			name: "flame graph URL without host",
			config: ProfilingConfig{
				AggregationInterval: 10,
				ExecutionTimeout:    20,
				FlameGraphBaseURL:   "http:///d",
			},
			wantError: "flame graph base url must include a host",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("Validate() error = %v, want contain %q", err, tt.wantError)
			}
		})
	}
}

func TestConfigValidateRejectsDuplicateUsers(t *testing.T) {
	cfg := Config{
		RuntimeCgroup: RuntimeCgroupConfig{LimitCPU: 1, LimitMem: 1},
		APIServer: APIServerConfig{
			TCPAddr:                  ":12740",
			ReadHeaderTimeoutSeconds: 10,
			ReadTimeoutSeconds:       30,
			WriteTimeoutSeconds:      60,
			IdleTimeoutSeconds:       120,
			ShutdownTimeoutSeconds:   60,
			MaxHeaderBytes:           1024,
			MaxBodyBytes:             1024,
			RateLimit:                10,
			RateBurst:                10,
		},
		TaskConfig: TaskConfig{
			MaxProfilingTasksPerHost: 1,
			MaxTracingTasksPerHost:   1,
			MaxTotalProfilingTasks:   1,
			MaxTotalTracingTasks:     1,
			JobStoreDSN:              "jobs.db",
			ShutdownConcurrency:      1,
		},
		Agent: AgentConfig{
			Port:                      19704,
			RequestTimeoutSeconds:     10,
			StatusRetryAttempts:       3,
			StatusRetryBackoffMillis:  100,
			StatusPollIntervalSeconds: 5,
			MaxConsecutivePollErrors:  3,
		},
		Profiling: ProfilingConfig{
			AggregationInterval: 10,
			ExecutionTimeout:    20,
			FlameGraphBaseURL:   "http://localhost:8006/d",
		},
		ElasticSearch: ElasticSearchConfig{Address: "http://127.0.0.1:9200"},
		Auth: AuthConfig{Users: []UserConfig{
			{ID: "duplicate", IsAdmin: true},
			{ID: "duplicate", IsAdmin: true},
		}},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate ID") {
		t.Fatalf("Validate() error=%v, want duplicate ID", err)
	}
}
