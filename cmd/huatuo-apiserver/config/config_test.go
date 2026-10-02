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

const requiredConfig = `
[Agent.Auth]
BearerToken = "node-secret"

[[Auth.Users]]
ID = "admin"
BearerToken = "user-secret"
Admin = true
`

func TestLoadFileDefaults(t *testing.T) {
	config := loadTestConfig(t, requiredConfig)

	if config.Log.Level != "Info" {
		t.Fatalf("Log.Level = %q, want Info", config.Log.Level)
	}
	if config.Runtime != (RuntimeConfig{CPULimitCores: 20, MemoryLimitMiB: 4096}) {
		t.Fatalf("Runtime = %+v", config.Runtime)
	}
	if config.APIServer.ListenAddress != ":12740" ||
		config.APIServer.RateLimit != (RateLimitConfig{200, 200}) {
		t.Fatalf("APIServer = %+v", config.APIServer)
	}
	if config.Jobs.Profiling != (JobQuotaConfig{3, 500}) ||
		config.Jobs.Tracing != (JobQuotaConfig{5, 1000}) {
		t.Fatalf("Jobs quotas = %+v", config.Jobs)
	}
	wantController := JobControllerConfig{
		StatusPollIntervalSeconds:         5,
		PendingTimeoutSeconds:             30,
		CompletionGracePeriodSeconds:      60,
		NodeUnavailableGracePeriodSeconds: 30,
		JobRetentionPeriodHours:           30 * 24,
	}
	if config.Jobs.Controller != wantController || config.Jobs.StoreDSN != "jobs.db" {
		t.Fatalf("Jobs lifecycle = %+v", config.Jobs)
	}
	if config.Agent.HTTPPort != 19704 || config.Agent.Auth.BearerToken != "node-secret" {
		t.Fatalf("Agent = %+v", config.Agent)
	}
	if config.Elasticsearch.Index != "huatuo_bamai" {
		t.Fatalf("Elasticsearch.Index = %q, want huatuo_bamai", config.Elasticsearch.Index)
	}
	if config.Profiling.DashboardBaseURL != "" {
		t.Fatalf("Profiling = %+v", config.Profiling)
	}
}

func TestLoadFileCanonicalOverrides(t *testing.T) {
	config := loadTestConfig(t, `
[Jobs]
StoreDSN = "state/jobs.db"

[Jobs.Profiling]
MaxConcurrentPerHost = 2
MaxConcurrent = 100

[Jobs.Tracing]
MaxConcurrentPerHost = 4
MaxConcurrent = 200

[Jobs.Controller]
StatusPollIntervalSeconds = 7
PendingTimeoutSeconds = 40
CompletionGracePeriodSeconds = 80
NodeUnavailableGracePeriodSeconds = 50
JobRetentionPeriodHours = 48

[Agent]
HTTPPort = 29704

[Agent.Auth]
BearerToken = "node-override"

[Profiling]
DashboardBaseURL = "https://grafana.example/d"

[[Auth.Users]]
ID = "operator"
BearerToken = "operator-secret"
Permissions = ["GET /v1/profiling/**"]
`)

	if config.Jobs.Profiling != (JobQuotaConfig{2, 100}) ||
		config.Jobs.Tracing != (JobQuotaConfig{4, 200}) {
		t.Fatalf("Jobs quotas = %+v", config.Jobs)
	}
	wantController := JobControllerConfig{7, 40, 80, 50, 48}
	if config.Jobs.Controller != wantController || config.Jobs.StoreDSN != "state/jobs.db" {
		t.Fatalf("Jobs lifecycle = %+v", config.Jobs)
	}
	if config.Agent.HTTPPort != 29704 || config.Agent.Auth.BearerToken != "node-override" {
		t.Fatalf("Agent = %+v", config.Agent)
	}
	if config.Profiling.DashboardBaseURL != "https://grafana.example/d" {
		t.Fatalf("Profiling = %+v", config.Profiling)
	}
}

func TestLoadFileRejectsRemovedConfiguration(t *testing.T) {
	removed := []string{
		`TaskConfig = { JobStoreDSN = "jobs.db" }`,
		`[Agent.StatusPolling]
IntervalSeconds = 5`,
		`[Profiling]
AggregationIntervalSeconds = 10`,
	}
	for _, value := range removed {
		t.Run(strings.Split(value, "\n")[0], func(t *testing.T) {
			if _, err := LoadFile(writeTestConfig(t, value+requiredConfig)); err == nil {
				t.Fatal("LoadFile() error = nil")
			}
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
			name: "profiling quota",
			mutate: func(config *Config) {
				config.Jobs.Profiling.MaxConcurrentPerHost = 0
			},
			wantErr: "profiling jobs per host",
		},
		{
			name: "controller polling interval",
			mutate: func(config *Config) {
				config.Jobs.Controller.StatusPollIntervalSeconds = 0
			},
			wantErr: "status poll interval seconds",
		},
		{
			name: "controller retention",
			mutate: func(config *Config) {
				config.Jobs.Controller.JobRetentionPeriodHours = 0
			},
			wantErr: "Job retention period hours",
		},
		{
			name: "Agent token missing",
			mutate: func(config *Config) {
				config.Agent.Auth.BearerToken = ""
			},
			wantErr: "Agent Auth BearerToken is required",
		},
		{
			name: "Agent token whitespace",
			mutate: func(config *Config) {
				config.Agent.Auth.BearerToken = "node secret"
			},
			wantErr: "must not contain whitespace",
		},
		{
			name: "dashboard scheme",
			mutate: func(config *Config) {
				config.Profiling.DashboardBaseURL = "ftp://grafana.example/d"
			},
			wantErr: "must use http or https",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := validConfig()
			tt.mutate(&config)
			if err := config.Validate(); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func validConfig() Config {
	config := defaultConfig()
	config.Agent.Auth.BearerToken = "node-secret"
	config.Auth.Users = []UserConfig{{
		ID:          "admin",
		BearerToken: "user-secret",
		Admin:       true,
	}}
	return config
}

func loadTestConfig(t *testing.T, contents string) *Config {
	t.Helper()
	config, err := LoadFile(writeTestConfig(t, contents))
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	return config
}

func writeTestConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "huatuo-apiserver.conf")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	return path
}
