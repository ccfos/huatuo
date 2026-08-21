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

func TestLoadFileDefaults(t *testing.T) {
	cfg := loadTestConfig(t, `
[[Auth.Users]]
ID = "admin"
BearerToken = "secret"
Admin = true
`)

	if cfg.Log.Level != "Info" {
		t.Errorf("Log.Level = %q, want %q", cfg.Log.Level, "Info")
	}
	if cfg.Runtime.CPULimitCores != 20 ||
		cfg.Runtime.MemoryLimitMiB != 4096 {
		t.Errorf("Runtime = %+v, want default limits", cfg.Runtime)
	}
	if cfg.APIServer.ListenAddress != ":12740" {
		t.Errorf("ListenAddress = %q, want %q", cfg.APIServer.ListenAddress, ":12740")
	}
	if cfg.APIServer.RateLimit.RequestsPerSecond != 200 ||
		cfg.APIServer.RateLimit.Burst != 200 {
		t.Errorf("RateLimit = %+v, want default values", cfg.APIServer.RateLimit)
	}
	if cfg.Jobs.Profiling != (JobQuotaConfig{3, 500}) {
		t.Errorf("Profiling quota = %+v, want default values", cfg.Jobs.Profiling)
	}
	if cfg.Jobs.Tracing != (JobQuotaConfig{5, 1000}) {
		t.Errorf("Tracing quota = %+v, want default values", cfg.Jobs.Tracing)
	}
	if cfg.Agent.HTTPPort != 19704 ||
		cfg.Agent.StatusPollingIntervalSeconds != 5 ||
		cfg.Agent.MaxConsecutiveStatusPollingErrors != 3 {
		t.Errorf("Agent = %+v, want default values", cfg.Agent)
	}
	if cfg.Elasticsearch.Enabled() {
		t.Error("Elasticsearch.Enabled() = true, want opt-in storage")
	}
	if cfg.Elasticsearch.Index != "huatuo_bamai" {
		t.Errorf("Elasticsearch.Index = %q, want default", cfg.Elasticsearch.Index)
	}
	if cfg.Profiling.AggregationIntervalSeconds != 10 ||
		cfg.Profiling.MaxConcurrentProfilerProcesses != 10 ||
		cfg.Profiling.DashboardBaseURL != "" {
		t.Errorf("Profiling = %+v, want default values", cfg.Profiling)
	}
}

func TestLoadFileCanonicalOverrides(t *testing.T) {
	cfg := loadTestConfig(t, `
[Log]
Level = "Warn"

[Runtime]
CPULimitCores = 8
MemoryLimitMiB = 2048

[APIServer]
ListenAddress = "127.0.0.1:18080"

[APIServer.RateLimit]
RequestsPerSecond = 20
Burst = 30

[Jobs]
StoreDSN = "state/jobs.db"

[Jobs.Profiling]
MaxConcurrentPerHost = 2
MaxConcurrent = 100

[Jobs.Tracing]
MaxConcurrentPerHost = 4
MaxConcurrent = 200

[Agent]
HTTPPort = 29704
RequestTimeoutSeconds = 20
StatusPollingIntervalSeconds = 7
MaxConsecutiveStatusPollingErrors = 4

[Elasticsearch]
Address = "https://search.example:9443"
Username = "huatuo"
Password = "secret"
Index = "profiles"

[Profiling]
AggregationIntervalSeconds = 15
MaxConcurrentProfilerProcesses = 6
DashboardBaseURL = "https://grafana.example/d"

[[Auth.Users]]
ID = "operator"
BearerToken = "operator-secret"
Permissions = ["GET /v1/profiling/**"]
`)

	if cfg.Runtime.MemoryLimitMiB != 2048 {
		t.Errorf("MemoryLimitMiB = %d, want 2048", cfg.Runtime.MemoryLimitMiB)
	}
	if cfg.Log.Level != "Warn" {
		t.Errorf("Log.Level = %q, want %q", cfg.Log.Level, "Warn")
	}
	if cfg.APIServer.RateLimit != (RateLimitConfig{20, 30}) {
		t.Errorf("RateLimit = %+v, want overrides", cfg.APIServer.RateLimit)
	}
	if cfg.Jobs.Profiling != (JobQuotaConfig{2, 100}) ||
		cfg.Jobs.Tracing != (JobQuotaConfig{4, 200}) {
		t.Errorf("Jobs = %+v, want quota overrides", cfg.Jobs)
	}
	if cfg.Agent.StatusPollingIntervalSeconds != 7 ||
		cfg.Agent.MaxConsecutiveStatusPollingErrors != 4 {
		t.Errorf("Agent = %+v, want status polling overrides", cfg.Agent)
	}
	if !cfg.Elasticsearch.Enabled() || cfg.Elasticsearch.Index != "profiles" {
		t.Errorf("Elasticsearch = %+v, want enabled overrides", cfg.Elasticsearch)
	}
	if cfg.Profiling.DashboardBaseURL != "https://grafana.example/d" {
		t.Errorf("DashboardBaseURL = %q, want override", cfg.Profiling.DashboardBaseURL)
	}
	if cfg.Auth.Users[0].ID != "operator" {
		t.Errorf("user ID = %q, want operator", cfg.Auth.Users[0].ID)
	}
}

func TestLoadFileRejectsLegacyKeys(t *testing.T) {
	tests := []struct {
		name     string
		contents string
	}{
		{
			name: "root log level",
			contents: `
LogLevel = "Info"

[[Auth.Users]]
ID = "admin"
BearerToken = "secret"
Admin = true
`,
		},
		{
			name: "task config",
			contents: `
[[Auth.Users]]
ID = "admin"
BearerToken = "secret"
Admin = true

[TaskConfig]
JobStoreDSN = "jobs.db"
`,
		},
		{
			name: "admin field",
			contents: `
[[Auth.Users]]
ID = "admin"
BearerToken = "secret"
IsAdmin = true
`,
		},
		{
			name: "http safeguard",
			contents: `
[[Auth.Users]]
ID = "admin"
BearerToken = "secret"
Admin = true

[APIServer]
ReadTimeoutSeconds = 60
`,
		},
		{
			name: "runtime cgroup section",
			contents: `
[[Auth.Users]]
ID = "admin"
BearerToken = "secret"
Admin = true

[RuntimeCgroup]
CPULimitCores = 20
`,
		},
		{
			name: "stop concurrency",
			contents: `
[[Auth.Users]]
ID = "admin"
BearerToken = "secret"
Admin = true

[Jobs]
MaxConcurrentStops = 16
`,
		},
		{
			name: "status polling section",
			contents: `
[[Auth.Users]]
ID = "admin"
BearerToken = "secret"
Admin = true

[Agent.StatusPolling]
IntervalSeconds = 5
MaxConsecutiveErrors = 3
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := LoadFile(writeTestConfig(t, tt.contents)); err == nil {
				t.Fatal("LoadFile() error = nil, want strict legacy-key rejection")
			}
		})
	}
}

func TestAuthConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		users   []UserConfig
		wantErr string
	}{
		{name: "missing users", wantErr: "at least one user"},
		{
			name: "missing ID",
			users: []UserConfig{{
				BearerToken: "secret",
				Admin:       true,
			}},
			wantErr: "id is required",
		},
		{
			name: "missing token",
			users: []UserConfig{{
				ID:    "admin",
				Admin: true,
			}},
			wantErr: "bearer token is required",
		},
		{
			name: "duplicate ID",
			users: []UserConfig{
				{ID: "same", BearerToken: "one", Admin: true},
				{ID: "same", BearerToken: "two", Admin: true},
			},
			wantErr: "duplicate id",
		},
		{
			name: "duplicate token",
			users: []UserConfig{
				{ID: "one", BearerToken: "same", Admin: true},
				{ID: "two", BearerToken: "same", Admin: true},
			},
			wantErr: "duplicate bearer token",
		},
		{
			name: "non-admin missing permissions",
			users: []UserConfig{{
				ID:          "viewer",
				BearerToken: "secret",
			}},
			wantErr: "permissions are required",
		},
		{
			name: "invalid permission method",
			users: []UserConfig{{
				ID:          "viewer",
				BearerToken: "secret",
				Permissions: []string{"FETCH /v1/jobs"},
			}},
			wantErr: "invalid permission method",
		},
		{
			name: "valid",
			users: []UserConfig{{
				ID:          "viewer",
				BearerToken: "secret",
				Permissions: []string{"GET /v1/jobs"},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := (AuthConfig{Users: tt.users}).Validate()
			assertErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name: "invalid log level",
			mutate: func(cfg *Config) {
				cfg.Log.Level = "verbose"
			},
			wantErr: "unsupported log level",
		},
		{
			name: "invalid CPU limit",
			mutate: func(cfg *Config) {
				cfg.Runtime.CPULimitCores = 0
			},
			wantErr: "cpu limit",
		},
		{
			name: "invalid listen address",
			mutate: func(cfg *Config) {
				cfg.APIServer.ListenAddress = "missing-port"
			},
			wantErr: "invalid listen address",
		},
		{
			name: "invalid profiling quota",
			mutate: func(cfg *Config) {
				cfg.Jobs.Profiling.MaxConcurrentPerHost = 0
			},
			wantErr: "profiling jobs per host",
		},
		{
			name: "invalid agent port",
			mutate: func(cfg *Config) {
				cfg.Agent.HTTPPort = 65536
			},
			wantErr: "must not exceed 65535",
		},
		{
			name: "invalid status polling interval",
			mutate: func(cfg *Config) {
				cfg.Agent.StatusPollingIntervalSeconds = 0
			},
			wantErr: "status polling interval",
		},
		{
			name: "invalid consecutive status polling errors",
			mutate: func(cfg *Config) {
				cfg.Agent.MaxConsecutiveStatusPollingErrors = 0
			},
			wantErr: "maximum consecutive status polling errors",
		},
		{
			name: "invalid aggregation interval",
			mutate: func(cfg *Config) {
				cfg.Profiling.AggregationIntervalSeconds = 1200
			},
			wantErr: "less than 1200 seconds",
		},
		{
			name: "invalid dashboard URL",
			mutate: func(cfg *Config) {
				cfg.Profiling.DashboardBaseURL = "ftp://grafana.example/d"
			},
			wantErr: "must use http or https",
		},
		{
			name: "incomplete Elasticsearch",
			mutate: func(cfg *Config) {
				cfg.Elasticsearch.Address = "https://search.example"
			},
			wantErr: "must be configured together",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.mutate(&cfg)
			assertErrorContains(t, cfg.Validate(), tt.wantErr)
		})
	}
}

func validConfig() Config {
	cfg := defaultConfig()
	cfg.Auth.Users = []UserConfig{{
		ID:          "admin",
		BearerToken: "secret",
		Admin:       true,
	}}
	return cfg
}

func loadTestConfig(t *testing.T, contents string) *Config {
	t.Helper()
	cfg, err := LoadFile(writeTestConfig(t, contents))
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	return cfg
}

func writeTestConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "huatuo-apiserver.conf")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	return path
}

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("error = %v, want nil", err)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want substring %q", err, want)
	}
}
